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
)

var applications = []diameter.Application{
	{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
	{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
}

// nodeManager owns the Diameter node. The node's identity comes from the
// operator settings, so a change of MCC/MNC replaces the node and its listener.
// The node dials the configured peers, and follows their changes in place.
type nodeManager struct {
	addresses []netip.Addr
	port      int
	mux       *diameter.Mux
	// metrics count and time the requests to the peers. They outlive the node.
	metrics *peerMetrics
	logger  *slog.Logger

	mu       sync.Mutex
	node     *diameter.Node
	listener *sctp.Listener
	replaced chan struct{}
	// peers are the configured peers that the node has.
	peers []settings.Peer
	// open are the configured peers that are open, by peer ID.
	open map[string]bool
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

// doHost addresses a request to a peer by its Destination-Host. For a host it
// has no peer for, such as a visited MME, the node falls back to the realm
// routes, then the default route, of the configured peers, which forward the
// request with its Destination-Host (RFC 6733 section 6.1). With none, it ends
// as an unknown peer. The default route never takes the SMSC's own realm.
func (m *nodeManager) doHost(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	addressed := *req
	addressed.AVPs = withDestinationHost(req.AVPs, host)

	ans, err := m.do(ctx, m.Node(), &addressed, diameter.FailFast())
	if errors.Is(err, diameter.ErrUnableToDeliver) {
		return nil, fmt.Errorf("%w %s: %w", diameter.ErrUnknownPeer, host, err)
	}

	return ans, err
}

// withDestinationHost sets the Destination-Host AVP, placed before
// Destination-Realm when the request has none.
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
		Logger:                  m.logger,
	})
	if err != nil {
		return err
	}

	// A new node has no peers yet.
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

// follow replaces the node whenever the operator settings change its identity,
// and gives it the configured peers whenever they change.
func (m *nodeManager) follow(ctx context.Context, live *settings.Live) {
	for {
		changed := live.Changed()
		current := live.Get()

		if operator := current.Operator; operator.Host() != m.Identity().OriginHost {
			m.logger.Info("restarting Diameter for the new identity; cores reconnect",
				slog.String("host", operator.Host()), slog.String("realm", operator.Realm()))

			m.shutdown(ctx)

			if err := m.start(ctx, operator, current.Peers); err != nil {
				m.logger.Error("failed to restart Diameter", slog.String("host", operator.Host()), slog.Any("error", err))
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

// setPeers gives the node its configured peers. The node keeps the connections
// of the peers that are unchanged, and only replaces their routes. m.peers are
// the peers the node has. The settings validate the peers as the node does, so
// SetPeers fails only on a bug: the node then keeps its peers, which differ from
// the settings, so the next change of settings tries again.
func (m *nodeManager) setPeers(node *diameter.Node, peers []settings.Peer) {
	// The peers are known before the node reports their state.
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
			ID:        p.NodeID(),
			Host:      p.Host,
			Addresses: p.Addresses,
			// S6c and SGd run over SCTP (3GPP TS 29.338 clause 4.6).
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

// peerStateChanged logs the configured peers that open and close. A configured
// peer is never an implicit route, so an HSS that is one serves S6c only
// through an s6c route to the operator's realm: it warns of one without.
func (m *nodeManager) peerStateChanged(operator settings.Operator, status diameter.PeerStatus) {
	if !status.Configured {
		return
	}

	m.mu.Lock()

	// A peer that is no longer configured may still report its closing.
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

// supports reports whether a peer negotiated an application: its applications
// are those common to both ends, a Relay advertisement standing for all of ours.
func supports(status diameter.PeerStatus, app uint32) bool {
	return slices.ContainsFunc(status.Applications, func(a diameter.Application) bool { return a.ID == app })
}

func routesS6c(p settings.Peer, realm string) bool {
	return slices.ContainsFunc(p.Routes, func(r settings.Route) bool {
		return r.Application == settings.ApplicationS6c && strings.EqualFold(r.Realm, realm)
	})
}

// listenAddresses are the addresses Diameter listens on and advertises: the
// configured address, or the addresses of the configured interface, such as a
// pod's eth0, whose address is not known in advance.
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
