package server

import (
	"context"
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
	address           netip.Addr
	port              int
	mux               *diameter.Mux
	onPeerStateChange func(diameter.PeerStatus)
	// metrics count and time the requests to the peers. They outlive the node.
	metrics *peerMetrics
	logger  *slog.Logger

	mu       sync.Mutex
	node     *diameter.Node
	listener *sctp.Listener
}

func (m *nodeManager) Node() *diameter.Node {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.node
}

// doHost sends a request to a peer, and counts and times it if it is on one of the SMSC's interfaces.
func (m *nodeManager) doHost(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	iface, ok := interfaceOf(req.ApplicationID)
	if !ok || m.metrics == nil {
		return m.Node().DoHost(ctx, host, req)
	}

	start := time.Now()
	ans, err := m.Node().DoHost(ctx, host, req)
	m.metrics.request(iface, peerResult(ans, err), time.Since(start))

	return ans, err
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
		OnPeerStateChange:       m.onPeerStateChange,
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
