package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
)

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

	logger.Info("smsc started", "db", cfg.DB.Path)

	<-ctx.Done()

	logger.Info("smsc stopping")

	return nil
}
