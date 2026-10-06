package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/settings"
	smscsgd "github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/ui"
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	attemptTimeout time.Duration

	database      *db.DB
	nodes         *nodeManager
	stopFollowing context.CancelFunc
	apiServer     *http.Server
	apiListener   net.Listener
	stopDelivery  context.CancelFunc
	deliveryDone  chan struct{}
}

func (s *Server) Start(ctx context.Context) error {
	if s.nodes != nil {
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

	initial, err := database.GetSettings(ctx)
	if err != nil {
		_ = database.Close()
		return err
	}

	live := settings.NewLive(database, initial)

	hss := newHSSRequester(live)
	mux := diameter.NewMux()

	nodes := &nodeManager{
		address:           cfg.Diameter.Address,
		port:              cfg.Diameter.Port,
		mux:               mux,
		onPeerStateChange: hss.peersChanged,
		logger:            s.Logger,
	}

	hss.nodes = nodes

	deliverer := &delivery.Deliverer{
		Store: database,
		Router: &smscs6c.Router{
			Node:     hss,
			Identity: nodes.Identity,
			Settings: live.Get,
		},
		Sender:         hostSender{nodes},
		Identity:       nodes.Identity,
		Settings:       live.Get,
		AttemptTimeout: cmp.Or(s.attemptTimeout, delivery.DefaultAttemptTimeout),
		Concurrency:    delivery.DefaultConcurrency,
		Now:            time.Now,
		Logger:         s.Logger,
	}

	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &smscsgd.Handler{
		Identity: nodes.Identity,
		Settings: live.Get,
		Store:    database,
		Stored:   deliverer.Notify,
		Now:      time.Now,
		Logger:   s.Logger,
	})
	mux.Handle(s6c.ApplicationID, s6c.CommandAlertServiceCentre, &smscs6c.AlertHandler{
		Identity: nodes.Identity,
		Settings: live.Get,
		Alert:    deliverer.Alert,
		Logger:   s.Logger,
	})

	if err := nodes.start(ctx, initial.Operator); err != nil {
		_ = database.Close()
		return err
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		nodes.shutdown(ctx)

		_ = database.Close()

		return fmt.Errorf("listen for the API: %w", err)
	}

	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler: api.NewHandler(api.Config{
			Store:    database,
			Diameter: diameterStatus{nodes: nodes, hss: hss},
			Frontend: ui.FS(),
			Settings: live,
			Notify:   deliverer.Notify,
			Now:      time.Now,
			Logger:   s.Logger,
		}),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() { _ = s.apiServer.Serve(apiLn) }()

	base := context.WithoutCancel(ctx)

	followCtx, stopFollowing := context.WithCancel(base)
	go nodes.follow(followCtx, live)

	deliveryCtx, stopDelivery := context.WithCancel(base)
	deliveryDone := make(chan struct{})

	go func() {
		deliverer.Run(deliveryCtx)
		close(deliveryDone)
	}()

	s.database = database
	s.nodes = nodes
	s.stopFollowing = stopFollowing
	s.stopDelivery = stopDelivery
	s.deliveryDone = deliveryDone

	s.Logger.Info("smsc started", "db", cfg.DB.Path, "diameter", nodes.Addr().String(), "api", apiLn.Addr().String())

	return nil
}

func (s *Server) APIAddr() net.Addr {
	if s.apiListener == nil {
		return nil
	}

	return s.apiListener.Addr()
}

func (s *Server) Addr() net.Addr {
	if s.nodes == nil {
		return nil
	}

	return s.nodes.Addr()
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.nodes == nil {
		return
	}

	s.Logger.Info("smsc stopping")

	s.stopFollowing()

	if err := s.apiServer.Shutdown(ctx); err != nil {
		s.Logger.Warn("failed to stop the API cleanly", slog.Any("error", err))
	}

	s.stopDelivery()

	select {
	case <-s.deliveryDone:
	case <-ctx.Done():
		s.Logger.Warn("shutting down with short message deliveries still in flight")
	}

	s.nodes.shutdown(ctx)

	if err := s.database.Close(); err != nil {
		s.Logger.Error("failed to close the database", slog.Any("error", err))
	}
}

type hssRequester struct {
	nodes    *nodeManager
	settings *settings.Live
	next     atomic.Uint32

	mu      sync.Mutex
	changed chan struct{}
}

func newHSSRequester(live *settings.Live) *hssRequester {
	return &hssRequester{settings: live, changed: make(chan struct{})}
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
	realm, hosts, err := r.waitForCandidates(ctx)
	if err != nil {
		return nil, err
	}

	setDestinationRealm(req, realm)

	start := r.next.Add(1)

	var (
		lastAns *diameter.Message
		lastErr error
	)

	for i := range uint32(len(hosts)) {
		host := hosts[(start+i)%uint32(len(hosts))]

		ans, err := r.nodes.Node().DoHost(ctx, host, req)

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

func (r *hssRequester) waitForCandidates(ctx context.Context) (string, []string, error) {
	for {
		changed := r.waitChan()

		realm, hosts := r.candidates()
		if len(hosts) > 0 {
			return realm, hosts, nil
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return "", nil, fmt.Errorf("%w in realm %s: %w", smscs6c.ErrNoHSS, realm, ctx.Err())
		}
	}
}

// setDestinationRealm keeps a request that waited for an HSS addressed to the
// realm its candidates were chosen from, in case the HSS realm changed meanwhile.
func setDestinationRealm(req *diameter.Message, realm string) {
	for i, a := range req.AVPs {
		if a.Code == diameter.AVPDestinationRealm && a.VendorID == 0 {
			req.AVPs[i] = diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, realm)
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

func (r *hssRequester) candidates() (string, []string) {
	var hosts []string

	realm := r.settings.Get().Operator.Realm()

	for _, p := range r.nodes.Node().Peers() {
		if p.State != diameter.PeerOpen || !strings.EqualFold(p.Realm, realm) {
			continue
		}

		if slices.ContainsFunc(p.Applications, func(a diameter.Application) bool { return a.ID == s6c.ApplicationID }) {
			hosts = append(hosts, p.Host)
		}
	}

	slices.Sort(hosts)

	return realm, hosts
}

type diameterStatus struct {
	nodes *nodeManager
	hss   *hssRequester
}

func (d diameterStatus) Identity() diameter.Identity { return d.nodes.Identity() }

func (d diameterStatus) Peers() []diameter.PeerStatus { return d.nodes.Node().Peers() }

func (d diameterStatus) Routes() []api.Route {
	realm, hosts := d.hss.candidates()

	return []api.Route{{Realm: realm, ApplicationID: s6c.ApplicationID, Peers: hosts}}
}

func (r *hssRequester) NewSessionID() string { return r.nodes.Node().NewSessionID() }

type hostSender struct{ nodes *nodeManager }

func (h hostSender) Do(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	return h.nodes.Node().DoHost(ctx, host, req)
}

func (h hostSender) NewSessionID() string { return h.nodes.Node().NewSessionID() }
