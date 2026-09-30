package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultPromoteInterval is how often a worker moves due retries into the stream
// and reclaims abandoned work.
const defaultPromoteInterval = 5 * time.Second

// WorkerOptions configures a Worker.
type WorkerOptions struct {
	Concurrency     int
	BlockTimeout    time.Duration
	StaleAfter      time.Duration
	ShutdownTimeout time.Duration
	// ConsumerName identifies this worker in the Redis consumer group. It defaults
	// to the hostname and pid so restarts leave distinguishable consumers.
	ConsumerName string
	// PromoteInterval overrides how often due retries are promoted and stale work
	// is reclaimed.
	PromoteInterval time.Duration
}

// Backend is the queue surface the worker depends on. Depending on the interface
// rather than the concrete Queue keeps the worker's dispatch, retry, and drain
// logic testable without a Redis server.
type Backend interface {
	EnsureGroup(ctx context.Context) error
	Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Delivery, error)
	RecoverStale(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]Delivery, error)
	Ack(ctx context.Context, delivery Delivery) error
	Fail(ctx context.Context, delivery Delivery, cause error) error
	PromoteDue(ctx context.Context, limit int64) (int, error)
}

var _ Backend = (*Queue)(nil)

// Worker runs a pool of consumers over a queue, dispatching each job to the
// handler registered for its type.
type Worker struct {
	queue    Backend
	handlers map[string]Handler
	opts     WorkerOptions
	logger   *slog.Logger
}

// NewWorker builds a Worker. Options are normalized so a partially configured
// worker still behaves sensibly.
func NewWorker(queue Backend, opts WorkerOptions, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.BlockTimeout <= 0 {
		opts.BlockTimeout = 2 * time.Second
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = 5 * time.Minute
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 15 * time.Second
	}
	if opts.PromoteInterval <= 0 {
		opts.PromoteInterval = defaultPromoteInterval
	}
	if opts.ConsumerName == "" {
		opts.ConsumerName = defaultConsumerName()
	}
	return &Worker{queue: queue, handlers: map[string]Handler{}, opts: opts, logger: logger}
}

func defaultConsumerName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// Register binds a handler to a job type. Registering a type twice replaces the
// previous handler, which keeps startup wiring order-independent.
func (w *Worker) Register(jobType string, handler Handler) {
	w.handlers[jobType] = handler
}

// Run consumes jobs until ctx is cancelled, then drains in-flight handlers.
//
// On cancellation the workers stop claiming new jobs but finish the job they are
// holding, bounded by ShutdownTimeout. Unfinished work stays in the Redis
// pending-entries list and is reclaimed by another worker through RecoverStale,
// so a shutdown never silently drops a job.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.queue.EnsureGroup(ctx); err != nil {
		return err
	}
	w.logger.Info("job worker started",
		slog.Int("concurrency", w.opts.Concurrency),
		slog.String("consumer", w.opts.ConsumerName),
		slog.Int("handlers", len(w.handlers)),
	)

	// The loop context follows shutdown, so consumers stop claiming new work as
	// soon as the signal arrives. The job context is deliberately detached from
	// it: shutdown must not abort a job mid-flight, only bound how long the
	// drain may take.
	loopCtx := ctx
	jobCtx, cancelJobs := context.WithTimeout(context.WithoutCancel(ctx), w.opts.ShutdownTimeout)
	defer cancelJobs()

	var wg sync.WaitGroup
	errCh := make(chan error, w.opts.Concurrency)
	for i := 0; i < w.opts.Concurrency; i++ {
		consumer := fmt.Sprintf("%s-%d", w.opts.ConsumerName, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.consume(loopCtx, jobCtx, consumer); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.maintain(loopCtx, jobCtx)
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			w.logger.Error("job consumer stopped", slog.Any("error", err))
		}
	}
	w.logger.Info("job worker drained")
	return nil
}

// consume reads and dispatches jobs until the loop context is cancelled.
func (w *Worker) consume(loopCtx, jobCtx context.Context, consumer string) error {
	ctx := loopCtx
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		deliveries, err := w.queue.Read(ctx, consumer, 1, w.opts.BlockTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if isRedisUnavailable(err) {
				// A Redis outage must not spin the loop; back off briefly.
				w.logger.Warn("job queue read failed", slog.Any("error", err))
				if !sleepCtx(ctx, time.Second) {
					return ctx.Err()
				}
				continue
			}
			return fmt.Errorf("read jobs: %w", err)
		}
		for _, delivery := range deliveries {
			w.handle(jobCtx, consumer, delivery)
		}
	}
}

// maintain promotes due retries and reclaims jobs abandoned by dead consumers.
// It follows the loop context so it stops as soon as shutdown begins, rather
// than idling until the drain timeout expires.
func (w *Worker) maintain(loopCtx, jobCtx context.Context) {
	ctx := loopCtx
	ticker := time.NewTicker(w.opts.PromoteInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			promoted, err := w.queue.PromoteDue(ctx, 100)
			if err != nil && ctx.Err() == nil {
				w.logger.Warn("promoting retries failed", slog.Any("error", err))
			} else if promoted > 0 {
				w.logger.Info("promoted delayed jobs", slog.Int("count", promoted))
			}
			recovered, err := w.queue.RecoverStale(ctx, "reclaimer", w.opts.StaleAfter, 50)
			if err != nil {
				if ctx.Err() == nil && !isRedisUnavailable(err) {
					w.logger.Warn("reclaiming stale jobs failed", slog.Any("error", err))
				}
				continue
			}
			for _, delivery := range recovered {
				w.logger.Info("reclaimed stale job",
					slog.String("stream_id", delivery.ID),
					slog.String("job_id", delivery.Job.ID),
					slog.String("type", delivery.Job.Type),
					slog.Int("attempts", delivery.Job.Attempts),
				)
				w.handle(jobCtx, "reclaimer", delivery)
			}
		}
	}
}

// handle dispatches one delivery and records its outcome.
func (w *Worker) handle(ctx context.Context, consumer string, delivery Delivery) {
	job := delivery.Job
	if job.Type == "" {
		// The payload could not be decrypted or parsed, so nothing can be done
		// with it beyond removing it from the queue.
		w.logger.Error("dropping unreadable job", slog.String("stream_id", delivery.ID))
		if err := w.queue.Fail(ctx, delivery, Permanent(errors.New("job payload is unreadable"))); err != nil {
			w.logger.Error("dead-lettering unreadable job failed", slog.Any("error", err))
		}
		return
	}

	started := time.Now()
	handler, ok := w.handlers[job.Type]
	if !ok {
		w.logger.Error("no handler registered for job type",
			slog.String("type", job.Type), slog.String("job_id", job.ID))
		if err := w.queue.Fail(ctx, delivery, Permanent(fmt.Errorf("no handler for %q", job.Type))); err != nil {
			w.logger.Error("dead-lettering job failed", slog.Any("error", err))
		}
		return
	}

	attrs := []any{
		slog.String("job_id", job.ID),
		slog.String("type", job.Type),
		slog.String("consumer", consumer),
		slog.Int("attempt", job.Attempts+1),
	}

	if err := handler(ctx, job); err != nil {
		attrs = append(attrs, slog.Any("error", err), slog.Duration("duration", time.Since(started)))
		w.logger.Warn("job failed", attrs...)
		if failErr := w.queue.Fail(ctx, delivery, err); failErr != nil {
			w.logger.Error("recording job failure failed", slog.Any("error", failErr))
		}
		return
	}
	if err := w.queue.Ack(ctx, delivery); err != nil {
		// The work is done but unacknowledged; it will be reclaimed and retried,
		// so handlers must be safe to run twice.
		w.logger.Error("acknowledging job failed", append(attrs, slog.Any("error", err))...)
		return
	}
	w.logger.Info("job completed", append(attrs, slog.Duration("duration", time.Since(started)))...)
}

// sleepCtx waits for d, reporting false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// isRedisUnavailable reports whether the error looks like a connection problem
// rather than a programming or protocol error.
func isRedisUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"connection refused", "connection reset", "i/o timeout",
		"no such host", "broken pipe", "eof", "context deadline exceeded"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
