package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/numbering"
	"github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/internal/tgpp"
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

	node := &diameter.Node{
		Identity: identity,
		Applications: []diameter.Application{
			{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
			{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
		},
		Peers: []diameter.Peer{{
			Host: cfg.HSS.Host,
			Address: &sctp.SCTPAddr{
				IPAddrs: []net.IPAddr{{IP: cfg.HSS.Address.AsSlice()}},
				Port:    cfg.HSS.Port,
			},
		}},
		Logger: s.Logger,
	}

	deliverer := &delivery.Deliverer{
		Store: database,
		Router: &s6c.Router{
			Node:                 node,
			Identity:             identity,
			HSSHost:              cfg.HSS.Host,
			HSSRealm:             cfg.HSS.Realm,
			ServiceCentreAddress: cfg.ServiceCentre.Address,
		},
		Sender:               node,
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		RetryIntervals:       cfg.Delivery.RetryIntervals,
		AttemptTimeout:       cfg.Delivery.AttemptTimeout,
		Concurrency:          cfg.Delivery.Concurrency,
		Now:                  time.Now,
		Logger:               s.Logger,
	}

	mux := diameter.NewMux()
	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &sgd.Handler{
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
	mux.Handle(s6c.ApplicationID, s6c.CommandAlertServiceCentre, &s6c.AlertHandler{
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		Alert:                deliverer.Alert,
		Logger:               s.Logger,
	})

	node.Handler = mux

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{
		IPAddrs: []net.IPAddr{{IP: cfg.Diameter.Address.AsSlice()}},
		Port:    cfg.Diameter.Port,
	})
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("listen for Diameter: %w", err)
	}

	base := context.WithoutCancel(ctx)

	if err := node.Serve(base, ln); err != nil {
		_ = ln.Close()
		_ = database.Close()

		return err
	}

	if err := node.Start(base); err != nil {
		shutdown, cancel := context.WithTimeout(base, time.Second)
		defer cancel()

		node.Shutdown(shutdown)

		_ = database.Close()

		return err
	}

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
	<-s.deliveryDone

	s.node.Shutdown(ctx)

	if err := s.database.Close(); err != nil {
		s.Logger.Error("failed to close the database", slog.Any("error", err))
	}
}
