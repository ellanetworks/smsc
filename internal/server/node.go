package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/settings"
	"go.opentelemetry.io/otel/trace"
)

var applications = []diameter.Application{
	{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
	{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
}

type nodeManager struct {
	addresses      []netip.Addr
	port           int
	mux            *diameter.Mux
	metrics        *peerMetrics
	tracerProvider trace.TracerProvider
	logger         *slog.Logger

	mu       sync.Mutex
	node     *diameter.Node
	listener *sctp.Listener
	replaced chan struct{}
	peers    []settings.Peer
	open     map[string]bool
}

func (m *nodeManager) Node() *diameter.Node {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.node
}

func (m *nodeManager) current() (*diameter.Node, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.replaced == nil {
		m.replaced = make(chan struct{})
	}

	return m.node, m.replaced
}

func (m *nodeManager) do(ctx context.Context, node *diameter.Node, req *diameter.Message, opts ...diameter.RequestOption) (*diameter.Message, error) {
	iface, ok := interfaceOf(req.ApplicationID)
	if !ok || m.metrics == nil {
		return node.Send(ctx, req, opts...)
	}

	start := time.Now()
	ans, err := node.Send(ctx, req, opts...)

	if !errors.Is(err, diameter.ErrNotConnected) && !errors.Is(err, diameter.ErrUnableToDeliver) {
		m.metrics.request(iface, peerResult(ans, err), time.Since(start))
	}

	return ans, err
}

// RFC 6733 section 6.1
func (m *nodeManager) doHost(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	addressed := *req
	addressed.AVPs = withDestinationHost(req.AVPs, host)

	ans, err := m.do(ctx, m.Node(), &addressed, diameter.FailFast())
	if errors.Is(err, diameter.ErrUnableToDeliver) {
		return nil, fmt.Errorf("%w %s: %w", diameter.ErrUnknownPeer, host, err)
	}

	return ans, err
}

func withDestinationHost(avps []diameter.AVP, host string) []diameter.AVP {
	destHost := diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, host)
	out := make([]diameter.AVP, 0, len(avps)+1)
	set := false

	for _, a := range avps {
		switch {
		case a.Code == diameter.AVPDestinationHost && a.VendorID == 0:
			if !set {
				out = append(out, destHost)
				set = true
			}

			continue
		case a.Code == diameter.AVPDestinationRealm && a.VendorID == 0 && !set:
			out = append(out, destHost)
			set = true
		}

		out = append(out, a)
	}

	if !set {
		out = append(out, destHost)
	}

	return out
}

func (m *nodeManager) Identity() diameter.Identity {
	return m.Node().Identity()
}

func (m *nodeManager) Addr() net.Addr {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.listener == nil {
		return nil
	}

	return m.listener.Addr()
}

func (m *nodeManager) start(ctx context.Context, operator settings.Operator, peers []settings.Peer) error {
	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      operator.Host(),
			OriginRealm:     operator.Realm(),
			HostIPAddresses: m.addresses,
			ProductName:     "smsc",
		},
		Handler:                 m.mux,
		AcceptUnknownPeers:      true,
		UnknownPeerApplications: applications,
		OnPeerStateChange:       func(status diameter.PeerStatus) { m.peerStateChanged(operator, status) },
		TracerProvider:          m.tracerProvider,
		Logger:                  m.logger,
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.peers, m.open = nil, nil
	m.mu.Unlock()

	m.setPeers(node, peers)

	laddr := &sctp.SCTPAddr{Port: m.port}
	for _, a := range m.addresses {
		laddr.IPAddrs = append(laddr.IPAddrs, net.IPAddr{IP: a.AsSlice()})
	}

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, laddr)
	if err != nil {
		_ = node.Shutdown(ctx)
		return fmt.Errorf("listen for Diameter: %w", err)
	}

	if addr, ok := ln.Addr().(*sctp.SCTPAddr); ok {
		m.port = addr.Port
	}

	m.mu.Lock()
	m.node, m.listener = node, ln

	if m.replaced != nil {
		close(m.replaced)
		m.replaced = nil
	}
	m.mu.Unlock()

	go func() { _ = node.Serve(diameter.NewSCTPListener(ln, m.logger)) }()

	return nil
}

func (m *nodeManager) shutdown(ctx context.Context) {
	if node := m.Node(); node != nil {
		_ = node.Shutdown(ctx)
	}
}

func (m *nodeManager) follow(ctx context.Context, live *settings.Live) {
	for {
		changed := live.Changed()
		current := live.Get()

		if operator := current.Operator; operator.Host() != m.Identity().OriginHost {
			m.logger.InfoContext(ctx, "restarting Diameter for the new identity; cores reconnect",
				slog.String("host", operator.Host()), slog.String("realm", operator.Realm()))

			m.shutdown(ctx)

			if err := m.start(ctx, operator, current.Peers); err != nil {
				m.logger.ErrorContext(ctx, "failed to restart Diameter", slog.String("host", operator.Host()), slog.Any("error", err))
			}
		} else if !slices.EqualFunc(current.Peers, m.configuredPeers(), settings.Peer.Equal) {
			m.setPeers(m.Node(), current.Peers)
		}

		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}

func (m *nodeManager) setPeers(node *diameter.Node, peers []settings.Peer) {
	m.mu.Lock()
	previous := m.peers
	m.peers = peers
	m.mu.Unlock()

	err := node.SetPeers(toDiameterPeers(peers))

	m.mu.Lock()
	defer m.mu.Unlock()

	if err != nil {
		m.logger.Error("failed to apply the Diameter peers", slog.Any("error", err))
		m.peers = previous

		return
	}

	for id := range m.open {
		if !slices.ContainsFunc(peers, func(p settings.Peer) bool { return p.NodeID() == id }) {
			delete(m.open, id)
		}
	}
}

func (m *nodeManager) configuredPeers() []settings.Peer {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.peers
}

func toDiameterPeers(peers []settings.Peer) []diameter.Peer {
	out := make([]diameter.Peer, 0, len(peers))

	for _, p := range peers {
		routes := make([]diameter.Route, 0, len(p.Routes))

		for _, r := range p.Routes {
			routes = append(routes, diameter.Route{
				Realm:       r.Realm,
				Application: applicationID(r.Application),
				Priority:    r.Priority,
				Default:     r.Default,
			})
		}

		out = append(out, diameter.Peer{
			ID:           p.NodeID(),
			Host:         p.Host,
			Addresses:    p.Addresses,
			Transports:   []diameter.Transport{diameter.TransportSCTP},
			Applications: applications,
			Routes:       routes,
			Dial:         &diameter.Dial{Port: uint16(p.Port)},
		})
	}

	return out
}

func applicationID(name string) uint32 {
	if name == settings.ApplicationS6c {
		return s6c.ApplicationID
	}

	return sgd.ApplicationID
}

func (m *nodeManager) peerStateChanged(operator settings.Operator, status diameter.PeerStatus) {
	if !status.Configured {
		return
	}

	m.mu.Lock()

	i := slices.IndexFunc(m.peers, func(p settings.Peer) bool { return p.NodeID() == status.ID })
	if i < 0 {
		m.mu.Unlock()
		return
	}

	cfg := m.peers[i]
	wasOpen := m.open[status.ID]
	isOpen := status.State == diameter.PeerOpen

	if m.open == nil {
		m.open = make(map[string]bool)
	}

	m.open[status.ID] = isOpen
	m.mu.Unlock()

	attrs := []any{slog.String("peer", status.ID), slog.String("host", status.Host), slog.String("realm", status.Realm)}

	switch {
	case isOpen && !wasOpen:
		m.logger.Info("Diameter peer open", attrs...)

		realm := operator.Realm()
		if strings.EqualFold(status.Realm, realm) && supports(status, s6c.ApplicationID) && !routesS6c(cfg, realm) {
			m.logger.Warn("Diameter peer in the operator's realm advertises S6c, but has no s6c route to it: "+
				"the SMSC sends it no routing requests", attrs...)
		}
	case !isOpen && wasOpen:
		m.logger.Info("Diameter peer closed", append(attrs, slog.String("error", status.LastError))...)
	}
}

func supports(status diameter.PeerStatus, app uint32) bool {
	return slices.ContainsFunc(status.Applications, func(a diameter.Application) bool { return a.ID == app })
}

func routesS6c(p settings.Peer, realm string) bool {
	return slices.ContainsFunc(p.Routes, func(r settings.Route) bool {
		return r.Application == settings.ApplicationS6c && strings.EqualFold(r.Realm, realm)
	})
}

func listenAddresses(cfg config.Diameter) ([]netip.Addr, error) {
	if cfg.Name == "" {
		return []netip.Addr{cfg.Address}, nil
	}

	iface, err := net.InterfaceByName(cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("diameter.name %s: %w", cfg.Name, err)
	}

	ifaddrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("diameter.name %s: %w", cfg.Name, err)
	}

	var addrs []netip.Addr

	for _, ifaddr := range ifaddrs {
		prefix, err := netip.ParsePrefix(ifaddr.String())
		if err != nil {
			continue
		}

		if a := prefix.Addr().Unmap(); !a.IsUnspecified() && !a.IsMulticast() && !a.IsLinkLocalUnicast() {
			addrs = append(addrs, a)
		}
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("diameter.name %s: the interface has no usable address", cfg.Name)
	}

	return addrs, nil
}
