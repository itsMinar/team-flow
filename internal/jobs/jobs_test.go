package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T, opts Options) *Queue {
	t.Helper()
	if opts.EncryptionKey == "" {
		opts.EncryptionKey = "test-key-material"
	}
	queue, err := NewQueue(unusedRedis(), opts)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return queue
}

// unusedRedis returns a client that is never dialed, so queue construction and
// payload handling can be exercised without a Redis server.
func unusedRedis() redis.UniversalClient {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
}

func TestPayloadsAreEncryptedAndAuthenticated(t *testing.T) {
	queue := newTestQueue(t, Options{})
	payload := map[string]string{"token": "tfk_super_secret", "invitation_id": "abc"}

	job := Job{ID: "job-1", Type: "invitation.email", MaxAttempts: 5}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = raw

	sealed, err := queue.seal(job)
	if err != nil {
		t.Fatal(err)
	}
	// The credential must not be readable from the stored value.
	if strings.Contains(sealed, "tfk_super_secret") || strings.Contains(sealed, "invitation.email") {
		t.Fatalf("sealed payload leaks its contents: %s", sealed)
	}
	opened, err := queue.open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.ID != job.ID || opened.Type != job.Type || string(opened.Payload) != string(raw) {
		t.Fatalf("round trip mismatch: %+v", opened)
	}

	// Tampering with the ciphertext is detected rather than silently accepted.
	tampered := "A" + sealed[1:]
	if _, err := queue.open(tampered); err == nil {
		t.Fatal("expected tampered payload to be rejected")
	}
	// A payload sealed with a different key cannot be read.
	other := newTestQueue(t, Options{EncryptionKey: "a-different-key"})
	otherSealed, err := other.seal(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.open(otherSealed); err == nil {
		t.Fatal("expected a payload from another key to be rejected")
	}
}

func TestNewQueueRequiresAnEncryptionKey(t *testing.T) {
	if _, err := NewQueue(unusedRedis(), Options{}); err == nil {
		t.Fatal("a queue must not exist without payload encryption")
	}
	if _, err := NewQueue(nil, Options{EncryptionKey: "k"}); err == nil {
		t.Fatal("a queue requires a redis client")
	}
}

func TestBackoffGrowsExponentiallyAndIsCapped(t *testing.T) {
	const base = 30 * time.Second
	const max = 10 * time.Minute
	queue := newTestQueue(t, Options{RetryBaseDelay: base, RetryMaxDelay: max})

	for attempt := 1; attempt <= 6; attempt++ {
		delay := queue.backoff(attempt)
		// Jitter adds at most half of the (capped) base delay, so the observed
		// delay sits between the exponential step and that step plus 50%.
		step := base << (attempt - 1)
		if step > max {
			step = max
		}
		if delay < step || delay > step+step/2 {
			t.Fatalf("attempt %d: backoff %v, want between %v and %v", attempt, delay, step, step+step/2)
		}
	}
	// Once the cap is reached, the delay never grows again.
	if delay := queue.backoff(20); delay > max+max/2 {
		t.Fatalf("backoff after the cap = %v", delay)
	}
	if first := queue.backoff(1); first < base || first > base+base/2 {
		t.Fatalf("first backoff = %v, want %v plus jitter", first, base)
	}
	// An invalid attempt count is treated as the first attempt.
	if queue.backoff(0) < base {
		t.Fatalf("backoff(0) = %v", queue.backoff(0))
	}
}

func TestPermanentErrorsAreRecognized(t *testing.T) {
	if err := Permanent(nil); err != nil {
		t.Fatalf("Permanent(nil) = %v, want nil", err)
	}
	base := context.Canceled
	wrapped := Permanent(base)
	if !IsPermanent(wrapped) {
		t.Fatal("wrapped error must be permanent")
	}
	if IsPermanent(base) {
		t.Fatal("a plain error must not be treated as permanent")
	}
	// The cause stays reachable for logging and errors.Is.
	if !strings.Contains(wrapped.Error(), context.Canceled.Error()) {
		t.Fatalf("error message = %q", wrapped.Error())
	}
}

func TestTruncateBoundsStoredErrors(t *testing.T) {
	long := strings.Repeat("x", maxErrorLength+50)
	if got := truncate(long, maxErrorLength); len(got) != maxErrorLength {
		t.Fatalf("length = %d, want %d", len(got), maxErrorLength)
	}
	short := "boom"
	if truncate(short, maxErrorLength) != short {
		t.Fatal("short messages must be unchanged")
	}
}

func TestNormalizesOptions(t *testing.T) {
	queue, err := NewQueue(unusedRedis(), Options{EncryptionKey: "k", MaxAttempts: 0, RetryBaseDelay: 0, RetryMaxDelay: -time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if queue.maxAttempts != 1 {
		t.Fatalf("maxAttempts = %d, want 1", queue.maxAttempts)
	}
	if queue.baseDelay != 30*time.Second {
		t.Fatalf("baseDelay = %v, want 30s", queue.baseDelay)
	}
	if queue.maxDelay != queue.baseDelay {
		t.Fatalf("maxDelay = %v, want the base delay", queue.maxDelay)
	}
}

func TestIsRedisUnavailable(t *testing.T) {
	if isRedisUnavailable(nil) {
		t.Fatal("nil is not an outage")
	}
	if !isRedisUnavailable(errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")) {
		t.Fatal("a refused connection is an outage")
	}
	if !isRedisUnavailable(errors.New("read tcp: i/o timeout")) {
		t.Fatal("a timeout is an outage")
	}
	if isRedisUnavailable(Permanent(errors.New("boom"))) {
		t.Fatal("a job error is not an outage")
	}
}

// fakeBackend serves a fixed script of deliveries so the worker's dispatch,
// retry, and drain behavior can be tested without Redis.
type fakeBackend struct {
	mu         sync.Mutex
	deliveries [][]Delivery
	acked      []Delivery
	failed     []struct {
		delivery Delivery
		cause    error
	}
	reads     int
	promoted  int
	recovered []Delivery
}

func (f *fakeBackend) EnsureGroup(context.Context) error { return nil }

func (f *fakeBackend) Read(_ context.Context, _ string, _ int64, block time.Duration) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if len(f.deliveries) == 0 {
		// Mimic a quiet queue: block briefly, then report nothing.
		time.Sleep(min(block, 5*time.Millisecond))
		return nil, nil
	}
	next := f.deliveries[0]
	f.deliveries = f.deliveries[1:]
	return next, nil
}

func (f *fakeBackend) RecoverStale(context.Context, string, time.Duration, int64) ([]Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recovered, nil
}

func (f *fakeBackend) Ack(_ context.Context, delivery Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, delivery)
	return nil
}

func (f *fakeBackend) Fail(_ context.Context, delivery Delivery, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, struct {
		delivery Delivery
		cause    error
	}{delivery: delivery, cause: cause})
	return nil
}

func (f *fakeBackend) PromoteDue(context.Context, int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.promoted, nil
}

func (f *fakeBackend) snapshot() (int, []Delivery, []error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	causes := make([]error, len(f.failed))
	for i, entry := range f.failed {
		causes[i] = entry.cause
	}
	return len(f.acked), append([]Delivery(nil), f.acked...), causes
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWorkerAcknowledgesSuccessfulJobs(t *testing.T) {
	backend := &fakeBackend{deliveries: [][]Delivery{{{
		ID: "1-0", Job: Job{ID: "job-1", Type: "email"},
	}}}}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())
	var ran int
	worker.Register("email", func(context.Context, Job) error {
		ran++
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, _, _ := backend.snapshot()
	if ran != 1 || acked != 1 {
		t.Fatalf("handler ran %d times, %d acks", ran, acked)
	}
}

func TestWorkerFailsJobsThatReturnErrors(t *testing.T) {
	backend := &fakeBackend{deliveries: [][]Delivery{{{
		ID: "1-0", Job: Job{ID: "job-1", Type: "email", Attempts: 1, MaxAttempts: 3},
	}}}}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())
	worker.Register("email", func(context.Context, Job) error {
		return errors.New("transport unavailable")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, _, failures := backend.snapshot()
	if acked != 0 || len(failures) != 1 || !strings.Contains(failures[0].Error(), "transport unavailable") {
		t.Fatalf("acks = %d, failures = %v", acked, failures)
	}
}

func TestWorkerDeadLettersPermanentFailures(t *testing.T) {
	backend := &fakeBackend{deliveries: [][]Delivery{{{
		ID: "1-0", Job: Job{ID: "job-1", Type: "gone"},
	}}}}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())
	worker.Register("gone", func(context.Context, Job) error {
		return Permanent(errors.New("target deleted"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, failures := backend.snapshot()
	if len(failures) != 1 || !IsPermanent(failures[0]) {
		t.Fatalf("failures = %v", failures)
	}
}

// An unregistered job type is a configuration error, so it is dead-lettered
// instead of retried forever.
func TestWorkerDeadLettersUnregisteredTypes(t *testing.T) {
	backend := &fakeBackend{deliveries: [][]Delivery{{{
		ID: "1-0", Job: Job{ID: "job-1", Type: "unknown"},
	}}}}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, failures := backend.snapshot()
	if len(failures) != 1 || !IsPermanent(failures[0]) {
		t.Fatalf("failures = %v", failures)
	}
}

// A job whose payload could not be decrypted can never succeed, so it is
// dead-lettered rather than retried.
func TestWorkerDeadLettersUnreadableJobs(t *testing.T) {
	backend := &fakeBackend{deliveries: [][]Delivery{{
		{ID: "1-0", Job: Job{LastError: "payload failed authentication"}},
	}}}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	acked, _, failures := backend.snapshot()
	if acked != 0 || len(failures) != 1 || !IsPermanent(failures[0]) {
		t.Fatalf("acks = %d, failures = %v", acked, failures)
	}
}

// Shutdown drains the job in flight instead of abandoning it.
func TestWorkerDrainsInFlightJobOnShutdown(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var finished bool
	backend := &fakeBackend{deliveries: [][]Delivery{{{
		ID: "1-0", Job: Job{ID: "job-1", Type: "slow"},
	}}}}
	worker := NewWorker(backend, WorkerOptions{
		Concurrency: 1, BlockTimeout: 5 * time.Millisecond, ShutdownTimeout: 2 * time.Second,
	}, testLogger())
	worker.Register("slow", func(ctx context.Context, _ Job) error {
		close(started)
		select {
		case <-ctx.Done():
			t.Error("the handler context must survive worker shutdown")
		case <-release:
		}
		finished = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	<-started
	cancel()
	// The worker must still be running because the handler is in flight.
	select {
	case <-done:
		t.Fatal("worker returned before the handler finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
	if !finished {
		t.Fatal("the in-flight handler did not finish")
	}
	acked, _, _ := backend.snapshot()
	if acked != 1 {
		t.Fatalf("the drained job was not acknowledged: %d", acked)
	}
}

// A Redis outage must not spin the consumer loop.
func TestWorkerBacksOffOnQueueOutage(t *testing.T) {
	backend := &failingBackend{}
	worker := NewWorker(backend, WorkerOptions{Concurrency: 1, BlockTimeout: 5 * time.Millisecond}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if backend.calls > 5 {
		t.Fatalf("consumer retried %d times without pausing", backend.calls)
	}
}

type failingBackend struct{ calls int }

func (f *failingBackend) EnsureGroup(context.Context) error { return nil }
func (f *failingBackend) Read(context.Context, string, int64, time.Duration) ([]Delivery, error) {
	f.calls++
	return nil, errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")
}
func (f *failingBackend) RecoverStale(context.Context, string, time.Duration, int64) ([]Delivery, error) {
	return nil, nil
}
func (f *failingBackend) Ack(context.Context, Delivery) error            { return nil }
func (f *failingBackend) Fail(context.Context, Delivery, error) error    { return nil }
func (f *failingBackend) PromoteDue(context.Context, int64) (int, error) { return 0, nil }

func TestWorkerRegistersAndReplacesHandlers(t *testing.T) {
	worker := NewWorker(newTestQueue(t, Options{}), WorkerOptions{}, nil)
	worker.Register("a", func(context.Context, Job) error { return nil })
	worker.Register("a", func(context.Context, Job) error { return Permanent(errors.New("boom")) })
	if len(worker.handlers) != 1 {
		t.Fatalf("handlers = %d, want 1", len(worker.handlers))
	}
	if !IsPermanent(worker.handlers["a"](context.Background(), Job{})) {
		t.Fatal("the later registration must win")
	}
}

func TestWorkerNormalizesOptions(t *testing.T) {
	worker := NewWorker(newTestQueue(t, Options{}), WorkerOptions{}, nil)
	if worker.opts.Concurrency != 1 || worker.opts.BlockTimeout != 2*time.Second ||
		worker.opts.ShutdownTimeout != 15*time.Second || worker.opts.StaleAfter != 5*time.Minute {
		t.Fatalf("unexpected defaults: %+v", worker.opts)
	}
	if worker.opts.ConsumerName == "" {
		t.Fatal("the consumer name must be set")
	}
}
