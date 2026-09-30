// Command worker is the TeamFlow background worker process. It consumes jobs
// from the Redis queue, runs the registered handlers, and retries or
// dead-letters work that fails.
//
// It shares the API's configuration, database, and Redis clients, so both
// processes see the same schema and the same queue.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/cache"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/jobs"
	"github.com/itsMinar/team-flow/internal/mailer"
	"github.com/itsMinar/team-flow/internal/observability"
	"github.com/itsMinar/team-flow/internal/organizations"
)

// sweepIntervals are how often each periodic maintenance job is re-scheduled
// after it runs.
const (
	invitationRedeliveryInterval = 5 * time.Minute
	apiKeyExpirySweepInterval    = 6 * time.Hour
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
	logger.Info("starting worker",
		slog.String("env", string(cfg.App.Env)),
		slog.Int("concurrency", cfg.Worker.Concurrency),
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

	queue, err := jobs.NewQueue(redisClient.Client, jobs.Options{
		EncryptionKey:  jobEncryptionKey(cfg),
		MaxAttempts:    cfg.Jobs.MaxAttempts,
		RetryBaseDelay: cfg.Jobs.RetryBaseDelay,
		RetryMaxDelay:  cfg.Jobs.RetryMaxDelay,
	})
	if err != nil {
		return fmt.Errorf("build job queue: %w", err)
	}

	// The worker owns email delivery: the API only queues the job.
	var mailSender mailer.Sender = mailer.NewLogSender(logger)
	if cfg.Mail.Transport == config.MailTransportNone {
		mailSender = mailer.DiscardSender{}
		logger.Warn("mail transport disabled; invitation emails will be dropped")
	}

	orgService := organizations.NewService(db.Pool, logger)
	invitationService := invitations.NewService(db.Pool, orgService, nil, mailSender, nil,
		cfg.Invite.TTL, cfg.Invite.BaseURL, logger)
	apiKeyService := apikeys.NewService(db.Pool, orgService, cfg.APIKey.DefaultTTL, cfg.APIKey.MaxTTL, logger)

	worker := jobs.NewWorker(queue, jobs.WorkerOptions{
		Concurrency:     cfg.Worker.Concurrency,
		BlockTimeout:    cfg.Worker.BlockTimeout,
		StaleAfter:      cfg.Worker.StaleAfter,
		ShutdownTimeout: cfg.Worker.ShutdownTimeout,
	}, logger)
	registerHandlers(ctx, queue, worker, invitationService, apiKeyService)

	if err := queue.EnsureGroup(ctx); err != nil {
		return fmt.Errorf("prepare job queue: %w", err)
	}
	if promoted, err := queue.PromoteDue(ctx, 100); err != nil {
		logger.Warn("could not promote delayed jobs at startup", slog.Any("error", err))
	} else if promoted > 0 {
		logger.Info("promoted delayed jobs at startup", slog.Int("count", promoted))
	}

	logger.Info("worker ready, processing jobs")
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// registerHandlers binds every job type the worker understands and seeds the
// first occurrence of the periodic maintenance jobs.
func registerHandlers(ctx context.Context, queue *jobs.Queue, worker *jobs.Worker,
	inviteSvc *invitations.Service, apiKeySvc *apikeys.Service,
) {
	worker.Register(invitations.JobTypeEmail, inviteSvc.EmailJobHandler)

	// Periodic jobs re-schedule themselves after a successful run, so a worker
	// restart resumes the schedule and an outage simply pauses it. Statuses are
	// always derived on read as well, so a missed sweep never serves stale data.
	worker.Register(invitations.JobTypeRedeliver, reschedule(ctx, queue, invitationRedeliveryInterval,
		inviteSvc.RedeliverJobHandler))
	worker.Register(apikeys.JobTypeExpireSweep, reschedule(ctx, queue, apiKeyExpirySweepInterval,
		apiKeySvc.ExpireSweepJobHandler))
	seed(ctx, queue, invitations.JobTypeRedeliver, invitationRedeliveryInterval)
	seed(ctx, queue, apikeys.JobTypeExpireSweep, apiKeyExpirySweepInterval)
}

// reschedule re-queues a periodic job after it runs. A failing run does not
// reschedule, because the queue already retries it with backoff.
func reschedule(ctx context.Context, queue *jobs.Queue, interval time.Duration, handler jobs.Handler) jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		if err := handler(ctx, job); err != nil {
			return err
		}
		_, err := queue.EnqueueIn(ctx, interval, job.Type, job.Payload)
		return err
	}
}

// seed schedules the first run of a periodic job, delayed by one interval so it
// does not compete with normal traffic at startup.
func seed(ctx context.Context, queue *jobs.Queue, jobType string, interval time.Duration) {
	if _, err := queue.EnqueueIn(ctx, interval, jobType, nil); err != nil {
		slog.Warn("could not schedule periodic job", slog.String("type", jobType), slog.Any("error", err))
	}
}

// jobEncryptionKey returns the key protecting job payloads at rest in Redis.
// JOB_ENCRYPTION_KEY is preferred; when it is absent the key is derived from the
// JWT secret so a working setup needs no extra configuration, at the cost of
// rotating both together.
func jobEncryptionKey(cfg *config.Config) string {
	if cfg.Jobs.EncryptionKey != "" {
		return cfg.Jobs.EncryptionKey
	}
	return "jwt/" + cfg.JWT.Secret
}
