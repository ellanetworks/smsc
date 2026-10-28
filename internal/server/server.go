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
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/settings"
	smscsgd "github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/ui"
	"github.com/ellanetworks/smsc/version"
)

var ErrAlreadyStarted = errors.New("server: already started")

type Server struct {
	Config config.Config
	Logger *slog.Logger

	attemptTimeout time.Duration
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
}

func (s *Server) Start(ctx context.Context) error {
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

	tlsConfig, err := apiTLS(cfg.API.TLS)
	if err != nil {
		return err
	}

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
		addresses: addresses,
		port:      cfg.Diameter.Port,
		mux:       mux,
		metrics:   m.peers,
		logger:    s.Logger,
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
		Now:            time.Now,
		Logger:         s.Logger,
	}

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
			Store:      database,
			Diameter:   diameterStatus{nodes: nodes, hss: hss},
			Frontend:   ui.FS(),
			Settings:   live,
			Notify:     deliverer.Notify,
			Ready:      func(ctx context.Context) error { return s.ready(ctx, database) },
			Received:   m.received,
			Metrics:    m.registry,
			Auth:       database,
			JWTSecret:  jwtSecret,
			BcryptCost: s.bcryptCost,
			// The cookie is Secure, and HSTS on, only over TLS: a browser would not send a Secure cookie over HTTP.
			SecureCookie: tlsConfig != nil,
			Now:          time.Now,
			Logger:       s.Logger,
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
	s.nodes = nodes
	s.stopFollowing = stopFollowing
	s.stopDelivery = stopDelivery
	s.deliveryDone = deliveryDone
	s.stopPurging = stopPurging
	s.purgeDone = purgeDone

	v := version.Get()

	s.Logger.Info("smsc started", "version", v.Version, "revision", v.Revision, "db", cfg.DB.Path,
		"diameter", nodes.Addr().String(), "api", apiLn.Addr().String(), "tls", tlsConfig != nil)

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

	s.draining.Store(true)

	if d := s.Config.API.DrainDelay; d > 0 {
		s.Logger.Info("draining the API", slog.Duration("delay", d))

		select {
		case <-time.After(d):
		case <-ctx.Done():
		}
	}

	s.stopFollowing()

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
	var hosts []string

	realm := h.realm()

	for _, r := range h.nodes.Node().Routes() {
		if r.Application != s6c.ApplicationID || !strings.EqualFold(r.Realm, realm) {
			continue
		}

		for _, p := range r.Peers {
			if p.State == diameter.PeerOpen {
				hosts = append(hosts, p.Host)
			}
		}
	}

	slices.Sort(hosts)

	return hosts
}

type diameterStatus struct {
	nodes *nodeManager
	hss   hssSender
}

func (d diameterStatus) Identity() diameter.Identity { return d.nodes.Identity() }

func (d diameterStatus) Peers() []diameter.PeerStatus { return d.nodes.Node().Peers() }

func (d diameterStatus) Routes() []api.Route {
	return []api.Route{{Realm: d.hss.realm(), ApplicationID: s6c.ApplicationID, Peers: d.hss.hosts()}}
}

func (h hssSender) NewSessionID() string { return h.nodes.Node().NewSessionID() }

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

// apiTLS returns the TLS configuration of the API, or nil for none. Go's defaults choose the cipher suites and key
// exchanges, which keeps the post-quantum hybrid key exchange that a list of curves would turn off.
func apiTLS(c *config.TLS) (*tls.Config, error) {
	if c == nil {
		return nil, nil //nolint:nilnil // No TLS is not an error.
	}

	cert, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, fmt.Errorf("load the API certificate: %w", err)
	}

	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
