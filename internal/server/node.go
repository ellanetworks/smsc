package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/settings"
)

var applications = []diameter.Application{
	{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
	{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
}

// nodeManager owns the Diameter node. The node's identity comes from the
// operator settings, so a change of MCC/MNC replaces the node and its listener.
type nodeManager struct {
	address netip.Addr
	port    int
	mux     *diameter.Mux
	// metrics count and time the requests to the peers. They outlive the node.
	metrics *peerMetrics
	logger  *slog.Logger

	mu       sync.Mutex
	node     *diameter.Node
	listener *sctp.Listener
	replaced chan struct{}
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

// doHost addresses a request to a peer by its Destination-Host. The node falls
// back to static realm routes for a host it has no peer for, and the SMSC has
// no routes, so that ends as an unknown peer.
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

func (m *nodeManager) start(ctx context.Context, operator settings.Operator) error {
	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      operator.Host(),
			OriginRealm:     operator.Realm(),
			HostIPAddresses: []netip.Addr{m.address},
			ProductName:     "smsc",
		},
		Handler:                 m.mux,
		AcceptUnknownPeers:      true,
		UnknownPeerApplications: applications,
		Logger:                  m.logger,
	})
	if err != nil {
		return err
	}

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: m.address.AsSlice()}}, Port: m.port})
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

// follow replaces the node whenever the operator settings change its identity.
func (m *nodeManager) follow(ctx context.Context, live *settings.Live) {
	for {
		changed := live.Changed()

		if operator := live.Get().Operator; operator.Host() != m.Identity().OriginHost {
			m.logger.Info("restarting Diameter for the new identity; cores reconnect",
				slog.String("host", operator.Host()), slog.String("realm", operator.Realm()))

			m.shutdown(ctx)

			if err := m.start(ctx, operator); err != nil {
				m.logger.Error("failed to restart Diameter", slog.String("host", operator.Host()), slog.Any("error", err))
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}
