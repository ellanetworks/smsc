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
	m := newMetrics(database)

	mux := diameter.NewMux()

	nodes := &nodeManager{
		address: cfg.Diameter.Address,
		port:    cfg.Diameter.Port,
		mux:     mux,
		metrics: m.peers,
		logger:  s.Logger,
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
			Store:    database,
			Diameter: diameterStatus{nodes: nodes, hss: hss},
			Frontend: ui.FS(),
			Settings: live,
			Notify:   deliverer.Notify,
			Received: m.received,
			Metrics:  m.registry,
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

	v := version.Get()

	s.Logger.Info("smsc started", "version", v.Version, "revision", v.Revision, "db", cfg.DB.Path,
		"diameter", nodes.Addr().String(), "api", apiLn.Addr().String())

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

func (h hostSender) NewSessionID() string { return h.nodes.Node().NewSessionID() }
