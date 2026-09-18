// Command worker is the TeamFlow background worker process. In Phase 1 it
// establishes the same dependency graph as the API (config, logging,
// PostgreSQL, Redis) and blocks until a shutdown signal is received. Job
// processing is implemented in a later phase.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"log/slog"

	"github.com/itsMinar/team-flow/internal/cache"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/observability"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := observability.NewLogger(cfg.Log.Level)
	logger.Info("starting worker", slog.String("env", string(cfg.App.Env)))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := database.New(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer db.Close()
	logger.Info("connected to postgres")

	redisClient, err := cache.New(ctx, cfg.Redis)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = redisClient.Close() }()
	logger.Info("connected to redis")

	logger.Info("worker ready, awaiting jobs")

	// Block until shutdown is requested. Future phases start a worker pool here
	// and drain in-flight jobs during shutdown.
	<-ctx.Done()
	logger.Info("shutdown signal received")
	logger.Info("shutdown complete")
	return nil
}
