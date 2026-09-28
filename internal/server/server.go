package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
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

	node, err := diameter.New(diameter.Config{
		Identity:                identity,
		Handler:                 mux,
		AcceptUnknownPeers:      true,
		UnknownPeerApplications: apps,
		Logger:                  s.Logger,
	})
	if err != nil {
		_ = database.Close()
		return err
	}

	deliverer := &delivery.Deliverer{
		Store: database,
		Router: &smscs6c.Router{
			Node:                 hssRequester{node},
			Identity:             identity,
			HSSHost:              cfg.HSS.Host,
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

	if err := node.SetPeers([]diameter.Peer{{
		ID:           hssPeerID,
		Host:         cfg.HSS.Host,
		Addresses:    []netip.Addr{cfg.HSS.Address},
		Port:         uint16(cfg.HSS.Port),
		Applications: apps,
	}}); err != nil {
		_ = ln.Close()
		_ = database.Close()

		return err
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

const hssPeerID = "hss"

type hssRequester struct{ node *diameter.Node }

func (r hssRequester) Do(ctx context.Context, _ string, req *diameter.Message) (*diameter.Message, error) {
	return r.node.Do(ctx, hssPeerID, req)
}

func (r hssRequester) NewSessionID() string { return r.node.NewSessionID() }

type hostSender struct{ node *diameter.Node }

func (h hostSender) Do(ctx context.Context, host string, req *diameter.Message) (*diameter.Message, error) {
	return h.node.DoHost(ctx, host, req)
}

func (h hostSender) NewSessionID() string { return h.node.NewSessionID() }
