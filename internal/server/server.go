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
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/esme"
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

	attemptTimeout time.Duration
	// tracerProvider, when set, traces the SMSC in place of the one of the configuration.
	tracerProvider trace.TracerProvider
	// shutdownTracing exports the spans that are left, and stops the tracing.
	shutdownTracing func(context.Context) error
	// bcryptCost is the cost of the password hashes: bcrypt's default when zero.
	bcryptCost int
	// purgeNow and purgeInterval are the clock and the period of the message retention: time.Now and purgeInterval
	// when zero.
	purgeNow      func() time.Time
	purgeInterval time.Duration

	database *db.DB
	// draining is set once Shutdown starts, and makes the API unready.
	draining      atomic.Bool
	nodes         *nodeManager
	stopFollowing context.CancelFunc
	apiServer     *http.Server
	apiListener   net.Listener
	stopDelivery  context.CancelFunc
	deliveryDone  chan struct{}
	stopPurging   context.CancelFunc
	purgeDone     chan struct{}
	// smppServer is the SMPP listener, nil when the configuration has none, and smppDone ends with its dispatcher.
	smppServer   *smpp.Server
	smppListener net.Listener
	stopSMPP     context.CancelFunc
	smppDone     chan struct{}
}

// tracingShutdownTimeout bounds how long Shutdown waits to export the spans that are left.
const tracingShutdownTimeout = 5 * time.Second

// smppShutdownTimeout bounds how long Shutdown waits for the ESMEs to answer unbind (SMPP 3.4 section 4.2).
const smppShutdownTimeout = 5 * time.Second

// smppInboundWindow is the most submit_sm outstanding on a session; one more is answered ESME_RTHROTTLED.
const smppInboundWindow = 64

// smppSystemID identifies the SMSC in bind responses (SMPP 3.4 section 5.2.1).
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

	initial, err := database.GetSettings(ctx)
	if err != nil {
		_ = database.Close()
		return err
	}

	jwtSecret, err := database.JWTSecret(ctx)
	if err != nil {
		_ = database.Close()
		return err
	}

	live := settings.NewLive(database, initial)
	m := newMetrics(database)

	mux := diameter.NewMux()

	nodes := &nodeManager{
		addresses:      addresses,
		port:           cfg.Diameter.Port,
		mux:            mux,
		metrics:        m.peers,
		tracerProvider: tp,
		logger:         s.Logger,
	}

	if tp == nil {
		// The node would otherwise trace with the global provider.
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
		TracerProvider: tp,
		Now:            time.Now,
		Logger:         s.Logger,
	}
	deliverer.Completed = esmes.NotificationsQueued

	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &smscsgd.Handler{
		Identity: nodes.Identity,
		Settings: live.Get,
		Store:    database,
		// A replace type message from an MS can replace an SMPP message, which queues its DELETED receipt.
		Stored: func() {
			deliverer.Notify()
			esmes.NotificationsQueued()
		},
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
			Store:      database,
			Diameter:   diameterStatus{nodes: nodes, hss: hss},
			Frontend:   ui.FS(),
			Settings:   live,
			Submitter:  submitter,
			Notify:     deliverer.Notify,
			ESMEs:      database,
			SMPP:       smppSessions{esmes},
			Ready:      func(ctx context.Context) error { return s.ready(ctx, database) },
			Received:   m.received,
			Metrics:    m.registry,
			Auth:       database,
			JWTSecret:  jwtSecret,
			BcryptCost: s.bcryptCost,
			// The cookie is Secure, and HSTS on, only over TLS: a browser would not send a Secure cookie over HTTP.
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
		if err := s.startSMPP(ctx, base, esmes, smppTLS); err != nil {
			_ = s.apiServer.Close()

			nodes.shutdown(ctx)

			_ = database.Close()

			return err
		}
	}

	followCtx, stopFollowing := context.WithCancel(base)
	go nodes.follow(followCtx, live)

	deliveryCtx, stopDelivery := context.WithCancel(base)
	deliveryDone := make(chan struct{})

	go func() {
		deliverer.Run(deliveryCtx)
		close(deliveryDone)
	}()

	purgeCtx, stopPurging := context.WithCancel(base)
	purgeDone := make(chan struct{})
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

	go func() {
		p.run(purgeCtx)
		close(purgeDone)
	}()

	s.database = database
	s.shutdownTracing = shutdownTracing
	s.nodes = nodes
	s.stopFollowing = stopFollowing
	s.stopDelivery = stopDelivery
	s.deliveryDone = deliveryDone
	s.stopPurging = stopPurging
	s.purgeDone = purgeDone

	v := version.Get()

	smppAddr := ""
	if s.smppListener != nil {
		smppAddr = s.smppListener.Addr().String()
	}

	s.Logger.Info("smsc started", "version", v.Version, "revision", v.Revision, "db", cfg.DB.Path,
		"diameter", nodes.Addr().String(), "api", apiLn.Addr().String(), "tls", tlsConfig != nil, "smpp", smppAddr,
		"smpp_tls", smppTLS != nil)

	return nil
}

// startSMPP starts the SMPP listener and its dispatcher of notifications.
func (s *Server) startSMPP(ctx, base context.Context, esmes *esme.Service, tlsConfig *tls.Config) error {
	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID:      smppSystemID,
		TLS:           tlsConfig,
		Authenticate:  esmes.Authenticate,
		Handler:       esmes,
		OnBound:       esmes.OnBound,
		OnClosed:      esmes.OnClosed,
		OnDecodeError: esmes.OnDecodeError,
		// Up to 64 submit_sm per session at once: SMPP 3.4 leaves the window to the SMSC and suggests 10 (sections
		// 2.5.2 and 2.6.2, notes). The database serializes the writes, so the window only overlaps parsing and I/O.
		// The outbound window is the default, 10, which the dispatcher fills.
		Session: smpp.SessionConfig{InboundWindow: smppInboundWindow},
		Logger:  s.Logger,
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

	dispatchCtx, stop := context.WithCancel(base)
	done := make(chan struct{})

	go func() {
		esmes.Run(dispatchCtx)
		close(done)
	}()

	s.smppServer, s.smppListener, s.stopSMPP, s.smppDone = srv, ln, stop, done

	return nil
}

// SMPPAddr is the address of the SMPP listener, or nil when there is none.
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

	s.Logger.Info("smsc stopping")

	s.draining.Store(true)

	if d := s.Config.API.DrainDelay; d > 0 {
		s.Logger.Info("draining the API", slog.Duration("delay", d))

		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}

	s.stopFollowing()

	// The SMPP listener stops first: the ESMEs are told to unbind, and the notifications they have not acknowledged
	// stay in the database for when they bind again.
	if s.smppServer != nil {
		smppCtx, cancel := context.WithTimeout(ctx, smppShutdownTimeout)
		if err := s.smppServer.Shutdown(smppCtx); err != nil {
			s.Logger.Warn("SMPP sessions closed without unbind", slog.Any("error", err))
		}

		cancel()
		s.stopSMPP()

		select {
		case <-s.smppDone:
		case <-ctx.Done():
		}
	}

	if err := s.apiServer.Shutdown(ctx); err != nil {
		s.Logger.Warn("failed to stop the API cleanly", slog.Any("error", err))
	}

	s.stopDelivery()
	s.stopPurging()

	select {
	case <-s.deliveryDone:
	case <-ctx.Done():
		s.Logger.Warn("shutting down with short message deliveries still in flight")
	}

	select {
	case <-s.purgeDone:
	case <-ctx.Done():
	}

	s.nodes.shutdown(ctx)

	if err := s.database.Close(); err != nil {
		s.Logger.Error("failed to close the database", slog.Any("error", err))
	}

	// The spans of the shutdown are the last to export, once the other steps may have used up ctx.
	tracingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tracingShutdownTimeout)
	defer cancel()

	if err := s.shutdownTracing(tracingCtx); err != nil {
		s.Logger.Warn("failed to export the last spans", slog.Any("error", err))
	}
}

// newTracerProvider returns the provider that traces the SMSC, nil when telemetry is off, and the function that
// stops it.
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

// ready is whether the API can serve: it is not once Shutdown starts, or while the database does not answer. The
// Diameter peers do not count: the Diameter Service selects the same pods, so an SMSC unready without its peers
// could not get them back.
func (s *Server) ready(ctx context.Context, database *db.DB) error {
	if s.draining.Load() {
		return errors.New("shutting down")
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

// hostsOf returns the hosts of the routes' peers that serve S6c in the
// operator's realm.
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

// servingPeers returns whether a peer of a route can take its requests: it is
// open, and negotiated the application, as the node checks before it sends.
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

		// An accepted peer is a route of an application only when it supports it.
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

// Routes are the route to the HSS, then the routes of the configured peers, with
// the default routes last. The route to the HSS takes the configured peers of
// its realm, and the connected HSSs.
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

// smppSessions is the SMPP listener as the API sees it.
type smppSessions struct{ esmes *esme.Service }

func (s smppSessions) CloseESME(id int64) { s.esmes.CloseESME(id) }

func (s smppSessions) CloseUser(ctx context.Context, id int64) error {
	return s.esmes.CloseUser(ctx, id)
}

func (s smppSessions) Sessions(id int64) []api.SMPPSession {
	var out []api.SMPPSession

	for _, sess := range s.esmes.Sessions(id) {
		out = append(out, api.SMPPSession{BindType: string(sess.BindType), Remote: sess.Remote.String(), BoundAt: sess.BoundAt})
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

// apiTLS returns the TLS configuration of the API or of the SMPP listener, or nil for none. Go's defaults choose the cipher suites and key
// exchanges, which keeps the post-quantum hybrid key exchange that a list of curves would turn off.
func apiTLS(c *config.TLS, what string) (*tls.Config, error) {
	if c == nil {
		return nil, nil //nolint:nilnil // No TLS is not an error.
	}

	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("load the %s certificate: %w", what, err)
	}

	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
