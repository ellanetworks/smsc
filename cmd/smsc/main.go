package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

const shutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "smsc.yaml", "path to the configuration file")

	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*configPath, logger); err != nil {
		logger.Error("smsc stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}

	defer func() { _ = database.Close() }()

	identity := diameter.Identity{
		OriginHost:      cfg.Diameter.OriginHost,
		OriginRealm:     cfg.Diameter.OriginRealm,
		HostIPAddresses: []netip.Addr{cfg.Diameter.Address},
		ProductName:     "smsc",
	}

	mux := diameter.NewMux()
	mux.Handle(sgd.ApplicationID, sgd.CommandMOForwardShortMessage, &sgd.Handler{
		Identity:             identity,
		ServiceCentreAddress: cfg.ServiceCentre.Address,
		Store:                database,
		Now:                  time.Now,
		Logger:               logger,
	})

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
		Handler: mux,
		Logger:  logger,
	}

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{
		IPAddrs: []net.IPAddr{{IP: cfg.Diameter.Address.AsSlice()}},
		Port:    cfg.Diameter.Port,
	})
	if err != nil {
		return fmt.Errorf("listen for Diameter: %w", err)
	}

	if err := node.Serve(ctx, ln); err != nil {
		return err
	}

	if err := node.Start(ctx); err != nil {
		return err
	}

	logger.Info("smsc started", "db", cfg.DB.Path, "diameter", ln.Addr().String())

	<-ctx.Done()

	logger.Info("smsc stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	node.Shutdown(shutdownCtx)

	return nil
}
