package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/logging"
	"github.com/ellanetworks/smsc/internal/server"
	"golang.org/x/sys/unix"
)

const shutdownTimeout = 5 * time.Second

func main() {
	configPath := flag.String("config", "smsc.yaml", "path to the configuration file")

	flag.Parse()

	if err := noCoreDumps(); err != nil {
		logging.New(os.Stdout, slog.LevelInfo).Error("smsc stopped", "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logging.New(os.Stdout, slog.LevelInfo).Error("smsc stopped", "error", err)
		os.Exit(1)
	}

	logger := logging.New(os.Stdout, cfg.Logging.Level)

	for _, w := range cfg.Warnings() {
		logger.Warn(w)
	}

	if err := run(cfg, logger); err != nil {
		logger.Error("smsc stopped", "error", err)
		os.Exit(1)
	}
}

func noCoreDumps() error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return fmt.Errorf("disable core dumps: %w", err)
	}

	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("disable core dumps: %w", err)
	}

	return nil
}

func run(cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &server.Server{Config: cfg, Logger: logger}

	if err := srv.Start(ctx); err != nil {
		return err
	}

	var failed error

	select {
	case <-ctx.Done():
	case failed = <-srv.Err():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), delivery.DefaultAttemptTimeout+shutdownTimeout)
	defer cancel()

	srv.Shutdown(shutdownCtx)

	return failed
}
