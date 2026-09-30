// Command api is the TeamFlow HTTP API process. It wires configuration,
// logging, PostgreSQL, Redis, routing, and graceful shutdown.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"log/slog"

	"github.com/itsMinar/team-flow/internal/api"
	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/cache"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/mailer"
	"github.com/itsMinar/team-flow/internal/observability"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

func main() {
	if err := run(); err != nil {
		// Fall back to slog default since our logger may not be initialized.
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
	logger.Info("starting api",
		slog.String("env", string(cfg.App.Env)),
		slog.Int("port", cfg.HTTP.Port),
	)

	// Root context cancelled on SIGINT/SIGTERM to coordinate shutdown.
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

	healthHandler := health.NewHandler(logger, map[string]health.Checker{
		"postgres": db,
		"redis":    redisClient,
	})

	// Authentication wiring: JWT signer, service, HTTP handler, and middleware.
	jwtService := auth.NewJWTService(cfg.JWT.Secret, cfg.JWT.Issuer, cfg.JWT.AccessTTL)
	authService := auth.NewService(db.Pool, jwtService, cfg.JWT.RefreshTTL, logger)
	authHandler := auth.NewHandler(authService, logger)

	orgService := organizations.NewService(db.Pool, logger)
	orgHandler := organizations.NewHandler(orgService, logger)
	orgMW := organizations.NewMiddleware(orgService, logger)
	teamService := teams.NewService(db.Pool, orgService, logger)
	teamHandler := teams.NewHandler(teamService, logger)
	projectService := projects.NewService(db.Pool, orgService)
	projectHandler := projects.NewHandler(projectService, logger)
	taskService := tasks.NewService(db.Pool, orgService)
	taskHandler := tasks.NewHandler(taskService, logger)

	// Invitation email: the log transport is for local development and tests.
	// Phase 10 adds the queued sender used in production.
	var mailSender mailer.Sender = mailer.NewLogSender(logger)
	if cfg.Mail.Transport == config.MailTransportNone {
		mailSender = mailer.DiscardSender{}
		logger.Warn("mail transport disabled; invitations will not be delivered")
	}
	invitationService := invitations.NewService(db.Pool, orgService, authService,
		mailSender, cfg.Invite.TTL, cfg.Invite.BaseURL, logger)
	invitationHandler := invitations.NewHandler(invitationService, logger)

	apiKeyService := apikeys.NewService(db.Pool, orgService, cfg.APIKey.DefaultTTL, cfg.APIKey.MaxTTL, logger)
	apiKeyHandler := apikeys.NewHandler(apiKeyService, logger)

	// The middleware is built last because it authenticates both bearer tokens
	// and API keys; every handler receives it when routes are registered.
	authMW := auth.NewMiddleware(jwtService, logger).WithAPIKeys(apiKeyService)

	router := api.NewRouter(api.Dependencies{
		Config:       cfg,
		Logger:       logger,
		Health:       healthHandler,
		AuthHandler:  authHandler,
		AuthMW:       authMW,
		OrgHandler:   orgHandler,
		OrgMW:        orgMW,
		TeamsHandler: teamHandler,
		Projects:     projectHandler,
		Tasks:        taskHandler,
		Invitations:  invitationHandler,
		APIKeys:      apiKeyHandler,
	})

	srv := &http.Server{
		Addr:         ":" + strconv.Itoa(cfg.HTTP.Port),
		Handler:      router,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
	}

	// Graceful shutdown: stop accepting new requests, let in-flight finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed, forcing close", slog.Any("error", err))
		_ = srv.Close()
		return err
	}

	logger.Info("shutdown complete")
	return nil
}
