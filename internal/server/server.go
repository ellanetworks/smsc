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
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/numbering"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
	smscsgd "github.com/ellanetworks/smsc/internal/sgd"
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	database     *db.DB
	node         *diameter.Node
	listener     *sctp.Listener
	stopDelivery context.CancelFunc
	deliveryDone chan struct{}
}

func (s *Server) Start(ctx context.Context) error {
	if s.node != nil {
		return ErrAlreadyStarted
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	cfg := s.Config

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	identity := diameter.Identity{
		OriginHost:      cfg.Diameter.OriginHost,
		OriginRealm:     cfg.Diameter.OriginRealm,
		HostIPAddresses: []netip.Addr{cfg.Diameter.Address},
		ProductName:     "smsc",
	}

	apps := []diameter.Application{
		{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
		{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
	}

	mux := diameter.NewMux()
	hss := newHSSRequester(cfg.HSS.Realm, cfg.HSS.AllowedNetworks)

	node, err := diameter.New(diameter.Config{
		Identity:                identity,
		Handler:                 mux,
		AcceptUnknownPeers:      true,
		UnknownPeerApplications: apps,
		OnPeerStateChange:       hss.peersChanged,
		Logger:                  s.Logger,
	})
	if err != nil {
		_ = database.Close()
		return err
	}

	hss.node = node

	deliverer := &delivery.Deliverer{
		Store: database,
		Router: &smscs6c.Router{
			Node:                 hss,
			Identity:             identity,
			HSSRealm:             cfg.HSS.Realm,
			ServiceCentreAddress: cfg.ServiceCentre.Address,
		},
		Sender:               hostSender{node},
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		RetryIntervals:       cfg.Delivery.RetryIntervals,
		AttemptTimeout:       cfg.Delivery.AttemptTimeout,
		Concurrency:          cfg.Delivery.Concurrency,
		Now:                  time.Now,
		Logger:               s.Logger,
	}

	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &smscsgd.Handler{
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		Store:                database,
		Numbering: numbering.Plan{
			CountryCode:         cfg.Numbering.CountryCode,
			NationalPrefix:      cfg.Numbering.NationalPrefix,
			InternationalPrefix: cfg.Numbering.InternationalPrefix,
		},
		DefaultValidity: cfg.Delivery.DefaultValidity,
		Stored:          deliverer.Notify,
		Now:             time.Now,
		Logger:          s.Logger,
	})
	mux.Handle(s6c.ApplicationID, s6c.CommandAlertServiceCentre, &smscs6c.AlertHandler{
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		Alert:                deliverer.Alert,
		Logger:               s.Logger,
	})

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{
		IPAddrs: []net.IPAddr{{IP: cfg.Diameter.Address.AsSlice()}},
		Port:    cfg.Diameter.Port,
	})
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("listen for Diameter: %w", err)
	}

	go func() { _ = node.Serve(diameter.NewSCTPListener(ln, s.Logger)) }()

	base := context.WithoutCancel(ctx)

	deliveryCtx, stopDelivery := context.WithCancel(base)
	deliveryDone := make(chan struct{})

	go func() {
		deliverer.Run(deliveryCtx)
		close(deliveryDone)
	}()

	s.database = database
	s.node = node
	s.listener = ln
	s.stopDelivery = stopDelivery
	s.deliveryDone = deliveryDone

	s.Logger.Info("smsc started", "db", cfg.DB.Path, "diameter", ln.Addr().String())

	return nil
}

func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}

	return s.listener.Addr()
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.node == nil {
		return
	}

	s.Logger.Info("smsc stopping")

	s.stopDelivery()

	select {
	case <-s.deliveryDone:
	case <-ctx.Done():
		s.Logger.Warn("shutting down with short message deliveries still in flight")
	}

	_ = s.node.Shutdown(ctx)

	if err := s.database.Close(); err != nil {
		s.Logger.Error("failed to close the database", slog.Any("error", err))
	}
}

var errNoHSS = errors.New("no HSS connected")

type hssRequester struct {
	node     *diameter.Node
	realm    string
	networks []netip.Prefix
	next     atomic.Uint32

	mu      sync.Mutex
	changed chan struct{}
}

func newHSSRequester(realm string, networks []netip.Prefix) *hssRequester {
	return &hssRequester{realm: realm, networks: networks, changed: make(chan struct{})}
}

func (r *hssRequester) peersChanged(diameter.PeerStatus) {
	r.mu.Lock()
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}

func (r *hssRequester) waitChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.changed
}

func (r *hssRequester) Do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	hosts, err := r.waitForCandidates(ctx)
	if err != nil {
		return nil, err
	}

	start := r.next.Add(1)

	var (
		lastAns *diameter.Message
		lastErr error
	)

	for i := range uint32(len(hosts)) {
		host := hosts[(start+i)%uint32(len(hosts))]

		ans, err := r.node.DoHost(ctx, host, req)

		switch {
		case err == nil && !isRelayFailure(ans):
			return ans, nil
		case err == nil:
			lastAns, lastErr = ans, nil
		case errors.Is(err, diameter.ErrNotConnected) || errors.Is(err, diameter.ErrUnknownPeer) ||
			errors.Is(err, diameter.ErrApplicationUnsupported):
			lastAns, lastErr = nil, err
		default:
			return nil, err
		}

		if ctx.Err() != nil {
			break
		}
	}

	if lastAns != nil {
		return lastAns, nil
	}

	return nil, lastErr
}

func (r *hssRequester) waitForCandidates(ctx context.Context) ([]string, error) {
	for {
		changed := r.waitChan()

		if hosts := r.candidates(); len(hosts) > 0 {
			return hosts, nil
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w in realm %s: %w", errNoHSS, r.realm, ctx.Err())
		}
	}
}

func isRelayFailure(ans *diameter.Message) bool {
	if ans.Flags&diameter.FlagError == 0 {
		return false
	}

	rc, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		return false
	}

	code, err := rc.Unsigned32()

	return err == nil && (code == diameter.ResultTooBusy || code == diameter.ResultUnableToDeliver)
}

func (r *hssRequester) candidates() []string {
	var hosts []string

	for _, p := range r.node.Peers() {
		if p.State != diameter.PeerOpen || !strings.EqualFold(p.Realm, r.realm) || !r.allowed(p.RemoteAddr) {
			continue
		}

		if slices.ContainsFunc(p.Applications, func(a diameter.Application) bool { return a.ID == s6c.ApplicationID }) {
			hosts = append(hosts, p.Host)
		}
	}

	slices.Sort(hosts)

	return hosts
}

func (r *hssRequester) allowed(addr netip.Addr) bool {
	if len(r.networks) == 0 {
		return true
	}

	addr = addr.Unmap()

	return slices.ContainsFunc(r.networks, func(n netip.Prefix) bool { return n.Contains(addr) })
}

func (r *hssRequester) NewSessionID() string { return r.node.NewSessionID() }

type hostSender struct{ node *diameter.Node }

func (h hostSender) Do(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	return h.node.DoHost(ctx, host, req)
}

func (h hostSender) NewSessionID() string { return h.node.NewSessionID() }
