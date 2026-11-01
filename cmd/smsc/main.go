package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/logging"
	"github.com/ellanetworks/smsc/internal/server"
)

const shutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "smsc.yaml", "path to the configuration file")

	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		logging.New(os.Stdout, slog.LevelInfo).Error("smsc stopped", "error", err)
		os.Exit(1)
	}

	logger := logging.New(os.Stdout, cfg.Logging.Level)

	if err := run(cfg, logger); err != nil {
		logger.Error("smsc stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &server.Server{Config: cfg, Logger: logger}

	if err := srv.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.API.DrainDelay+delivery.DefaultAttemptTimeout+shutdownTimeout)
	defer cancel()

	srv.Shutdown(shutdownCtx)

	return nil
}
