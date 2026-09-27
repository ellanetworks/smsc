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
	"github.com/ellanetworks/smsc/internal/sgd"
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

	server := &diameter.Server{
		Identity:     identity,
		Applications: []diameter.Application{{ID: sgd.ApplicationID, VendorID: sgd.VendorID3GPP}},
		Handler: &sgd.Handler{
			Identity:             identity,
			ServiceCentreAddress: cfg.ServiceCentre.Address,
			Store:                database,
			Now:                  time.Now,
			Logger:               logger,
		},
		Logger: logger,
	}

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{
		IPAddrs: []net.IPAddr{{IP: cfg.Diameter.Address.AsSlice()}},
		Port:    cfg.Diameter.Port,
	})
	if err != nil {
		return fmt.Errorf("listen for Diameter: %w", err)
	}

	if err := server.Serve(ctx, ln); err != nil {
		return err
	}

	logger.Info("smsc started", "db", cfg.DB.Path, "diameter", ln.Addr().String())

	<-ctx.Done()

	logger.Info("smsc stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	server.Shutdown(shutdownCtx)

	return nil
}
