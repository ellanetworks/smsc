package server

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/esme"
	"github.com/ellanetworks/smsc/internal/oidc"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/settings"
	smscsgd "github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/internal/tracing"
	"github.com/ellanetworks/smsc/smpp"
	"github.com/ellanetworks/smsc/ui"
	"github.com/ellanetworks/smsc/version"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	attemptTimeout  time.Duration
	tracerProvider  trace.TracerProvider
	shutdownTracing func(context.Context) error
	bcryptCost      int
	purgeNow        func() time.Time
	purgeInterval   time.Duration

	database      *db.DB
	draining      atomic.Bool
	nodes         *nodeManager
	stopFollowing context.CancelFunc
	apiServer     *http.Server
	apiListener   net.Listener
	node          *cluster.Node
	deliverer     *delivery.Deliverer
	esmes         *esme.Service
	drain         *drainer
	stopLeading   context.CancelFunc
	engines       sync.WaitGroup
	smppServer    *smpp.Server
	smppListener  net.Listener
}

const tracingShutdownTimeout = 5 * time.Second

const smppShutdownTimeout = 5 * time.Second

const smppInboundWindow = 64

const smppSystemID = "EllaSMSC"

func (s *Server) Start(ctx context.Context) (err error) {
	if s.nodes != nil {
		return ErrAlreadyStarted
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	cfg := s.Config

	addresses, err := listenAddresses(cfg.Diameter)
	if err != nil {
		return err
	}

	tlsConfig, err := apiTLS(cfg.API.TLS, "API")
	if err != nil {
		return err
	}

	var smppTLS *tls.Config

	if cfg.SMPP != nil {
		if smppTLS, err = apiTLS(cfg.SMPP.TLS, "SMPP"); err != nil {
			return err
		}
	}

	tp, shutdownTracing, err := s.newTracerProvider(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			_ = shutdownTracing(context.WithoutCancel(ctx))
		}
	}()

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	clusterCfg := cluster.Config{
		Dir: filepath.Join(filepath.Dir(cfg.DB.Path), "raft"), Version: version.Get().Version, Now: time.Now,
		Logger: s.Logger,
	}

	if c := cfg.Cluster; c != nil {
		clusterCfg.Address = netip.AddrPortFrom(c.Address, uint16(c.Port)).String()
		clusterCfg.Advertise = c.AdvertiseAddress()
	}

	node, err := cluster.Open(ctx, clusterCfg, database)
	if err != nil {
		_ = database.Close()
		return err
	}

	database.SetProposer(node)

	closeStore := func() {
		if err := errors.Join(node.Close(), database.Close()); err != nil {
			s.Logger.ErrorContext(ctx, "failed to close the database", slog.Any("error", err))
		}
	}

	initial, err := database.GetSettings(ctx)
	if err != nil {
		closeStore()
		return err
	}

	secrets, err := newSecrets(ctx, database)
	if err != nil {
		closeStore()
		return err
	}

	oidcSettings, err := database.GetOIDC(ctx)
	if err != nil {
		closeStore()
		return err
	}

	live := settings.NewLive(database, initial)

	m, err := newMetrics(database, node)
	if err != nil {
		closeStore()
		return err
	}

	mux := diameter.NewMux()

	nodes := &nodeManager{
		addresses:      addresses,
		port:           cfg.Diameter.Port,
		mux:            mux,
		metrics:        m.peers,
		tracerProvider: tp,
		logger:         s.Logger,
		label:          settings.NodeLabel(node.ID()),
	}

	if tp == nil {
		nodes.tracerProvider = noop.NewTracerProvider()
	}

	hss := hssSender{nodes: nodes, settings: live}
	m.watchHSS(hss)

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
		Metrics:        m.delivery,
		TracerProvider: tp,
		Now:            time.Now,
		Logger:         s.Logger,
	}

	submitter := &submission.Submitter{Store: database, Settings: live.Get, Received: m.received, Now: time.Now}
	esmes := &esme.Service{
		Store:          database,
		Submitter:      submitter,
		Settings:       live.Get,
		Notify:         deliverer.Notify,
		Received:       m.received,
		Metrics:        m.smpp,
		BcryptCost:     s.bcryptCost,
		Node:           node.ID(),
		TracerProvider: tp,
		Now:            time.Now,
		Logger:         s.Logger,
	}

	s.drain = &drainer{
		node: node, store: database, esmes: esmes, logger: s.Logger,
		wake: make(chan struct{}, 1),
	}

	oidcLive := oidc.NewLive(database, oidcSettings)

	database.OnEvents(func(ev db.Events) {
		s.applyEvents(context.WithoutCancel(ctx), ev, database, live, oidcLive, deliverer, esmes, secrets)
	})

	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &smscsgd.Handler{
		Identity: nodes.Identity,
		Settings: live.Get,
		Store:    database,
		Stored:   deliverer.Notify,
		Received: m.received,
		Now:      time.Now,
		Logger:   s.Logger,
	})
	mux.Handle(s6c.ApplicationID, s6c.CommandAlertServiceCentre, &smscs6c.AlertHandler{
		Identity: nodes.Identity,
		Settings: live.Get,
		Alert:    deliverer.Alert,
		Logger:   s.Logger,
	})

	if err := nodes.start(ctx, initial.Operator, initial.Peers); err != nil {
		closeStore()
		return err
	}

	var apiLC net.ListenConfig

	apiLn, err := apiLC.Listen(ctx, "tcp", netip.AddrPortFrom(cfg.API.Address, uint16(cfg.API.Port)).String())
	if err != nil {
		nodes.shutdown(ctx)

		closeStore()

		return fmt.Errorf("listen for the API: %w", err)
	}

	s.apiListener = apiLn
	s.apiServer = &http.Server{
		Handler: api.NewHandler(api.Config{
			Store:          database,
			Diameter:       diameterStatus{nodes: nodes, hss: hss},
			Frontend:       ui.FS(),
			Settings:       live,
			Submitter:      submitter,
			Notify:         deliverer.Notify,
			ESMEs:          database,
			OIDC:           oidcLive,
			Backups:        node,
			Cluster:        node,
			SMPP:           smppSessions{database},
			Ready:          func(ctx context.Context) error { return s.ready(ctx, database, node) },
			Received:       m.received,
			Metrics:        m.registry,
			Requests:       m.requests,
			Auth:           database,
			JWTSecret:      secrets.jwt,
			OIDCCookieKey:  secrets.cookieKey,
			BcryptCost:     s.bcryptCost,
			SecureCookie:   tlsConfig != nil,
			TracerProvider: tp,
			Now:            time.Now,
			Logger:         s.Logger,
		}),
		ErrorLog:          slog.NewLogLogger(s.Logger.Handler(), slog.LevelWarn),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
		TLSConfig:         tlsConfig,
	}

	if tlsConfig != nil {
		go func() { _ = s.apiServer.ServeTLS(apiLn, "", "") }()
	} else {
		go func() { _ = s.apiServer.Serve(apiLn) }()
	}

	base := context.WithoutCancel(ctx)

	if cfg.SMPP != nil {
		if err := s.startSMPP(ctx, esmes, smppTLS); err != nil {
			_ = s.apiServer.Close()

			nodes.shutdown(ctx)

			closeStore()

			return err
		}
	}

	followCtx, stopFollowing := context.WithCancel(base)
	go nodes.follow(followCtx, live)

	if cfg.SMPP != nil {
		go esmes.RunSessions(followCtx)
		go esmes.Run(followCtx)
	}

	p := &purger{
		store:    database,
		settings: live.Get,
		interval: cmp.Or(s.purgeInterval, purgeInterval),
		now:      s.purgeNow,
		logger:   s.Logger,
	}

	if p.now == nil {
		p.now = time.Now
	}

	leadCtx, stopLeading := context.WithCancel(base)

	node.Lead(func(ctx context.Context, term uint64) {
		s.engines.Add(1)
		defer s.engines.Done()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		stop := context.AfterFunc(leadCtx, cancel)
		defer stop()

		var wg sync.WaitGroup

		wg.Go(func() { deliverer.Run(ctx, term) })
		wg.Go(func() { p.run(ctx) })

		wg.Go(func() { cleanSessions(ctx, database, s.Logger) })
		wg.Go(func() { expireNotifications(ctx, esmes) })
		wg.Go(func() { dropLostSMPPSessions(ctx, node, database, s.Logger) })

		wg.Wait()
	})

	go s.fenceOnRemoval(base, node, nodes, s.smppServer)

	go s.drain.run(followCtx)

	s.database = database
	s.deliverer = deliverer
	s.esmes = esmes
	s.node = node
	s.shutdownTracing = shutdownTracing
	s.nodes = nodes
	s.stopFollowing = stopFollowing
	s.stopLeading = stopLeading

	v := version.Get()

	smppAddr := ""
	if s.smppListener != nil {
		smppAddr = s.smppListener.Addr().String()
	}

	s.Logger.InfoContext(ctx, "smsc started", "version", v.Version, "revision", v.Revision, "db", cfg.DB.Path,
		"diameter", nodes.Addr().String(), "api", apiLn.Addr().String(), "tls", tlsConfig != nil, "smpp", smppAddr,
		"smpp_tls", smppTLS != nil)

	return nil
}

func (s *Server) startSMPP(ctx context.Context, esmes *esme.Service, tlsConfig *tls.Config) error {
	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID:      smppSystemID,
		TLS:           tlsConfig,
		Authenticate:  esmes.Authenticate,
		Handler:       esmes,
		OnBound:       esmes.OnBound,
		OnClosed:      esmes.OnClosed,
		OnDecodeError: esmes.OnDecodeError,
		OnRefused:     esmes.OnRefused,
		Session:       smpp.SessionConfig{InboundWindow: smppInboundWindow},
		Logger:        s.Logger,
	})
	if err != nil {
		return err
	}

	esmes.Accounts = srv

	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", netip.AddrPortFrom(s.Config.SMPP.Address, uint16(s.Config.SMPP.Port)).String())
	if err != nil {
		return fmt.Errorf("listen for SMPP: %w", err)
	}

	go func() { _ = srv.Serve(ln) }()

	s.smppServer, s.smppListener = srv, ln

	return nil
}

func (s *Server) SMPPAddr() net.Addr {
	if s.smppListener == nil {
		return nil
	}

	return s.smppListener.Addr()
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

	s.Logger.InfoContext(ctx, "smsc stopping")

	s.draining.Store(true)

	s.stopLeading()

	if err := s.node.HandOver(); err != nil {
		s.Logger.WarnContext(ctx, "failed to hand the leadership over", slog.Any("error", err))
	}

	s.stopFollowing()

	if s.smppServer != nil {
		smppCtx, cancel := context.WithTimeout(ctx, smppShutdownTimeout)
		if err := s.smppServer.Shutdown(smppCtx); err != nil {
			s.Logger.WarnContext(ctx, "SMPP sessions closed without unbind", slog.Any("error", err))
		}

		cancel()
	}

	if err := s.apiServer.Shutdown(ctx); err != nil {
		s.Logger.WarnContext(ctx, "failed to stop the API cleanly", slog.Any("error", err))
	}

	engines := make(chan struct{})

	go func() {
		s.engines.Wait()
		close(engines)
	}()

	select {
	case <-engines:
	case <-ctx.Done():
	}

	if err := errors.Join(s.deliverer.Wait(ctx), s.esmes.Wait(ctx)); err != nil {
		s.Logger.WarnContext(ctx, "shutting down with short message deliveries still in flight")
	}

	s.nodes.shutdown(ctx)

	if err := errors.Join(s.node.Close(), s.database.Close()); err != nil {
		s.Logger.ErrorContext(ctx, "failed to close the database", slog.Any("error", err))
	}

	tracingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tracingShutdownTimeout)
	defer cancel()

	if err := s.shutdownTracing(tracingCtx); err != nil {
		s.Logger.WarnContext(ctx, "failed to export the last spans", slog.Any("error", err))
	}
}

func (s *Server) newTracerProvider(ctx context.Context) (trace.TracerProvider, func(context.Context) error, error) {
	none := func(context.Context) error { return nil }

	switch {
	case s.tracerProvider != nil:
		return s.tracerProvider, none, nil
	case !s.Config.Telemetry.Enabled:
		return nil, none, nil
	}

	tp, err := tracing.NewProvider(ctx, s.Config.Telemetry.OTLPEndpoint, version.Get().Version, s.Logger)
	if err != nil {
		return nil, nil, fmt.Errorf("start tracing: %w", err)
	}

	return tp, tp.Shutdown, nil
}

func (s *Server) ready(ctx context.Context, database *db.DB, node *cluster.Node) error {
	if s.draining.Load() {
		return errors.New("shutting down")
	}

	if err := node.Ready(); err != nil {
		return err
	}

	return database.Ping(ctx)
}

type hssSender struct {
	nodes    *nodeManager
	settings *settings.Live
}

func (h hssSender) Do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	for {
		node, replaced := h.nodes.current()
		realm := node.Identity().OriginRealm

		setDestinationRealm(req, realm)

		ans, err := h.nodes.do(ctx, node, req)

		switch {
		case errors.Is(err, diameter.ErrClosed):
			select {
			case <-replaced:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("%w in realm %s: %w", smscs6c.ErrNoHSS, realm, ctx.Err())
			}
		case errors.Is(err, diameter.ErrNotConnected) || errors.Is(err, diameter.ErrUnableToDeliver):
			return nil, fmt.Errorf("%w in realm %s: %w", smscs6c.ErrNoHSS, realm, err)
		}

		return ans, err
	}
}

func setDestinationRealm(req *diameter.Message, realm string) {
	for i, a := range req.AVPs {
		if a.Code == diameter.AVPDestinationRealm && a.VendorID == 0 {
			req.AVPs[i] = diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, realm)
		}
	}
}

func (h hssSender) realm() string { return h.settings.Get().Operator.Realm() }

func (h hssSender) hosts() []string {
	node := h.nodes.Node()

	return h.hostsOf(node.Routes(), servingPeers(node))
}

func (h hssSender) hostsOf(routes []diameter.RouteStatus, serving func(diameter.RoutePeerStatus, uint32) bool) []string {
	var hosts []string

	realm := h.realm()

	for _, r := range routes {
		if r.Application != s6c.ApplicationID || !strings.EqualFold(r.Realm, realm) {
			continue
		}

		for _, p := range r.Peers {
			if serving(p, r.Application) {
				hosts = append(hosts, p.Host)
			}
		}
	}

	slices.Sort(hosts)

	return hosts
}

func servingPeers(node *diameter.Node) func(diameter.RoutePeerStatus, uint32) bool {
	apps := make(map[string][]diameter.Application)

	for _, p := range node.Peers() {
		if p.ID != "" {
			apps[p.ID] = p.Applications
		}
	}

	return func(p diameter.RoutePeerStatus, app uint32) bool {
		if p.State != diameter.PeerOpen {
			return false
		}

		if p.Implicit {
			return true
		}

		return slices.ContainsFunc(apps[p.ID], func(a diameter.Application) bool { return a.ID == app })
	}
}

type diameterStatus struct {
	nodes *nodeManager
	hss   hssSender
}

func (d diameterStatus) Identity() diameter.Identity { return d.nodes.Identity() }

func (d diameterStatus) Peers() []diameter.PeerStatus { return d.nodes.Node().Peers() }

func (d diameterStatus) Peer(id string) (diameter.PeerStatus, bool) { return d.nodes.Node().Peer(id) }

func (d diameterStatus) Routes() []api.Route {
	node := d.nodes.Node()
	table, serving := node.Routes(), servingPeers(node)
	realm := d.hss.realm()
	routes := []api.Route{{Realm: realm, ApplicationID: s6c.ApplicationID, Peers: d.hss.hostsOf(table, serving)}}

	var defaults []api.Route

	for _, r := range table {
		if r.Application == s6c.ApplicationID && strings.EqualFold(r.Realm, realm) {
			continue
		}

		route := api.Route{Realm: r.Realm, ApplicationID: r.Application, Default: r.Default}
		configured := false

		for _, p := range r.Peers {
			if p.Implicit {
				continue
			}

			configured = true

			if serving(p, r.Application) {
				route.Peers = append(route.Peers, p.Host)
			}
		}

		switch {
		case !configured:
		case r.Default:
			defaults = append(defaults, route)
		default:
			routes = append(routes, route)
		}
	}

	return append(routes, defaults...)
}

func (h hssSender) NewSessionID() string { return h.nodes.Node().NewSessionID() }

type smppSessions struct{ store *db.DB }

func (s smppSessions) Sessions(id int64) []api.SMPPSession {
	sessions, err := s.store.SMPPSessions(context.Background(), id)
	if err != nil {
		return nil
	}

	out := make([]api.SMPPSession, 0, len(sessions))

	for _, sess := range sessions {
		out = append(out, api.SMPPSession{BindType: sess.BindType, Remote: sess.Remote, BoundAt: sess.BoundAt})
	}

	return out
}

type hostSender struct{ nodes *nodeManager }

func (h hostSender) Do(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	return h.nodes.doHost(ctx, host, req)
}

func (h hostSender) PeerRealm(host string) (string, bool) {
	for _, p := range h.nodes.Node().Peers() {
		if p.State == diameter.PeerOpen && strings.EqualFold(p.Host, host) {
			return p.Realm, true
		}
	}

	return "", false
}

func (h hostSender) NewSessionID() string { return h.nodes.Node().NewSessionID() }

func apiTLS(c *config.TLS, what string) (*tls.Config, error) {
	if c == nil {
		return nil, nil //nolint:nilnil
	}

	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("load the %s certificate: %w", what, err)
	}

	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
