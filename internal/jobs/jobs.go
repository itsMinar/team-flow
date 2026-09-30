// Package jobs implements Phase 10: a Redis-backed background job queue with a
// worker pool, retries with exponential backoff, and dead-letter handling.
//
// The queue is built on Redis Streams and consumer groups. Streams give
// at-least-once delivery with an acknowledgement step, a pending-entries list
// that identifies abandoned work, and the ability to reclaim messages from a
// consumer that died — the properties a job queue needs and that a plain list
// or sorted set cannot provide without reimplementing them.
//
// Job payloads are encrypted with AES-GCM before they are written to Redis.
// Job payloads carry credentials, such as the one-time token in an invitation
// email, so a Redis dump or an operator with read access to the queue must not
// hand out live credentials.
package jobs

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Redis keys. Namespacing keeps the queue separable from any other Redis use.
const (
	streamKey = "teamflow:jobs:stream"
	retryKey  = "teamflow:jobs:retry"
	deadKey   = "teamflow:jobs:dead"
	groupName = "teamflow-workers"

	// payloadField is the single field of each stream entry.
	payloadField = "job"
	// deadStreamMaxLen trims the dead-letter stream so a stuck job type cannot
	// grow Redis without bound.
	deadStreamMaxLen = 10000
	// maxErrorLength bounds what is stored in the retry record.
	maxErrorLength = 500
)

// Enqueuer is the subset of the queue that producers need. Services depend on
// this interface rather than on the concrete Queue so a producer can be tested
// without Redis.
type Enqueuer interface {
	Enqueue(ctx context.Context, jobType string, payload any) (Job, error)
	EnqueueIn(ctx context.Context, delay time.Duration, jobType string, payload any) (Job, error)
}

// Handler processes one job. Returning an error schedules a retry unless the
// error is permanent or the attempt budget is exhausted.
type Handler func(ctx context.Context, job Job) error

// Job is the unit of work. It is stored encrypted, so the queue never writes the
// payload in the clear.
type Job struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
	EnqueuedAt  time.Time       `json:"enqueued_at"`
	LastError   string          `json:"last_error,omitempty"`
}

// Delivery is a claimed job together with the stream entry ID needed to
// acknowledge it.
type Delivery struct {
	ID  string
	Job Job
}

// permanentError marks a failure that must never be retried, such as a job whose
// target no longer exists.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the worker dead-letters the job instead of retrying it.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err was marked permanent.
func IsPermanent(err error) bool {
	var target *permanentError
	return errors.As(err, &target)
}

// Options configures a Queue.
type Options struct {
	// EncryptionKey protects job payloads at rest. Any non-empty string works;
	// it is stretched to a 32-byte key. Callers that do not set one must pass a
	// derived key, because unencrypted payloads are never written.
	EncryptionKey  string
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// Queue enqueues, claims, retries, and dead-letters jobs. It is safe for
// concurrent use by multiple worker processes.
type Queue struct {
	rdb         redis.UniversalClient
	aead        cipher.AEAD
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// NewQueue builds a Queue. The encryption key is required: job payloads may
// contain credentials, so there is no unencrypted mode.
func NewQueue(rdb redis.UniversalClient, opts Options) (*Queue, error) {
	if rdb == nil {
		return nil, errors.New("jobs: redis client is required")
	}
	aead, err := newAEAD(opts.EncryptionKey)
	if err != nil {
		return nil, err
	}
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	if opts.RetryBaseDelay <= 0 {
		opts.RetryBaseDelay = 30 * time.Second
	}
	if opts.RetryMaxDelay < opts.RetryBaseDelay {
		opts.RetryMaxDelay = opts.RetryBaseDelay
	}
	return &Queue{
		rdb: rdb, aead: aead, maxAttempts: opts.MaxAttempts,
		baseDelay: opts.RetryBaseDelay, maxDelay: opts.RetryMaxDelay,
	}, nil
}

// newAEAD stretches the configured key material into a 32-byte AES key. The
// domain separation label keeps this key independent of any other derived key.
func newAEAD(keyMaterial string) (cipher.AEAD, error) {
	if keyMaterial == "" {
		return nil, errors.New("jobs: an encryption key is required for job payloads")
	}
	sum := sha256.Sum256([]byte("teamflow/jobs/v1\x00" + keyMaterial))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("jobs: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("jobs: build aead: %w", err)
	}
	return aead, nil
}

func (q *Queue) seal(job Job) (string, error) {
	raw, err := json.Marshal(job)
	if err != nil {
		return "", fmt.Errorf("jobs: encode job: %w", err)
	}
	nonce := make([]byte, q.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("jobs: generate nonce: %w", err)
	}
	sealed := q.aead.Seal(nonce, nonce, raw, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (q *Queue) open(sealed string) (Job, error) {
	blob, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return Job{}, fmt.Errorf("jobs: decode payload: %w", err)
	}
	nonceSize := q.aead.NonceSize()
	if len(blob) < nonceSize {
		return Job{}, errors.New("jobs: payload is truncated")
	}
	raw, err := q.aead.Open(nil, blob[:nonceSize], blob[nonceSize:], nil)
	if err != nil {
		return Job{}, errors.New("jobs: payload failed authentication")
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return Job{}, fmt.Errorf("jobs: decode job: %w", err)
	}
	return job, nil
}

// Enqueue adds a job to the stream for immediate processing.
func (q *Queue) Enqueue(ctx context.Context, jobType string, payload any) (Job, error) {
	return q.enqueue(ctx, jobType, payload, 0)
}

// EnqueueIn schedules a job to become claimable after delay. Delayed work is
// held in a sorted set and promoted by the workers, so nothing has to block or
// poll Redis per job.
func (q *Queue) EnqueueIn(ctx context.Context, delay time.Duration, jobType string, payload any) (Job, error) {
	if delay < 0 {
		delay = 0
	}
	return q.enqueue(ctx, jobType, payload, delay)
}

func (q *Queue) enqueue(ctx context.Context, jobType string, payload any, delay time.Duration) (Job, error) {
	if jobType == "" {
		return Job{}, errors.New("jobs: a job type is required")
	}
	job := Job{
		ID: uuid.NewString(), Type: jobType,
		MaxAttempts: q.maxAttempts, EnqueuedAt: time.Now().UTC(),
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Job{}, fmt.Errorf("jobs: encode job payload: %w", err)
		}
		job.Payload = raw
	}
	sealed, err := q.seal(job)
	if err != nil {
		return Job{}, err
	}

	if delay > 0 {
		due := time.Now().Add(delay).UnixMilli()
		if err := q.rdb.ZAdd(ctx, retryKey, redis.Z{Score: float64(due), Member: sealed}).Err(); err != nil {
			return Job{}, fmt.Errorf("jobs: schedule job: %w", err)
		}
		return job, nil
	}
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey, Values: map[string]any{payloadField: sealed},
	}).Err(); err != nil {
		return Job{}, fmt.Errorf("jobs: enqueue: %w", err)
	}
	return job, nil
}

// EnsureGroup creates the consumer group, ignoring the error that occurs when it
// already exists.
func (q *Queue) EnsureGroup(ctx context.Context) error {
	err := q.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "0").Err()
	if err != nil && !isBusyGroup(err) {
		return fmt.Errorf("jobs: create consumer group: %w", err)
	}
	return nil
}

// isBusyGroup reports the "group already exists" error Redis returns when a
// worker starts against a queue that is already running.
func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}

// Read claims up to count jobs for the consumer, blocking for at most block.
// redis.Nil means nothing was available before the timeout, which is not an
// error for a worker loop.
func (q *Queue) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Delivery, error) {
	streams, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: groupName, Consumer: consumer,
		Streams: []string{streamKey, ">"}, Count: count, Block: block,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var deliveries []Delivery
	for _, stream := range streams {
		for _, message := range stream.Messages {
			sealed, ok := message.Values[payloadField].(string)
			if !ok {
				continue
			}
			job, err := q.open(sealed)
			if err != nil {
				// An unreadable entry can never succeed, so hand it back with an
				// empty job and let the caller dead-letter it.
				deliveries = append(deliveries, Delivery{ID: message.ID, Job: Job{LastError: err.Error()}})
				continue
			}
			deliveries = append(deliveries, Delivery{ID: message.ID, Job: job})
		}
	}
	return deliveries, nil
}

// RecoverStale claims jobs left unacknowledged by a consumer that stopped, so a
// worker crash does not strand work in the pending-entries list.
func (q *Queue) RecoverStale(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]Delivery, error) {
	start := "0-0"
	messages, _, err := q.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: streamKey, Group: groupName, Consumer: consumer,
		MinIdle: minIdle, Start: start, Count: count,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	var deliveries []Delivery
	for _, message := range messages {
		sealed, ok := message.Values[payloadField].(string)
		if !ok {
			continue
		}
		job, err := q.open(sealed)
		if err != nil {
			deliveries = append(deliveries, Delivery{ID: message.ID, Job: Job{LastError: err.Error()}})
			continue
		}
		deliveries = append(deliveries, Delivery{ID: message.ID, Job: job})
	}
	return deliveries, nil
}

// Ack confirms a job is done and removes the stream entry so the stream does not
// grow without bound.
func (q *Queue) Ack(ctx context.Context, delivery Delivery) error {
	pipe := q.rdb.Pipeline()
	pipe.XAck(ctx, streamKey, groupName, delivery.ID)
	pipe.XDel(ctx, streamKey, delivery.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("jobs: acknowledge job: %w", err)
	}
	return nil
}

// Fail records a failed attempt. The job is rescheduled with exponential backoff
// and jitter, or moved to the dead-letter stream once the attempt budget is
// exhausted or the failure is permanent. The original entry is always
// acknowledged, so a failing job cannot be retried forever.
func (q *Queue) Fail(ctx context.Context, delivery Delivery, cause error) error {
	job := delivery.Job
	job.Attempts++
	job.LastError = truncate(cause.Error(), maxErrorLength)

	sealed, err := q.seal(job)
	if err != nil {
		return err
	}

	pipe := q.rdb.Pipeline()
	if IsPermanent(cause) || job.MaxAttempts <= 0 || job.Attempts >= job.MaxAttempts {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: deadKey, MaxLen: deadStreamMaxLen, Approx: true,
			Values: map[string]any{payloadField: sealed},
		})
	} else {
		due := time.Now().Add(q.backoff(job.Attempts)).UnixMilli()
		pipe.ZAdd(ctx, retryKey, redis.Z{Score: float64(due), Member: sealed})
	}
	pipe.XAck(ctx, streamKey, groupName, delivery.ID)
	pipe.XDel(ctx, streamKey, delivery.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("jobs: record job failure: %w", err)
	}
	return nil
}

// PromoteDue moves retries whose backoff has elapsed into the stream. Workers
// call it periodically, so a delayed job needs no timer of its own.
func (q *Queue) PromoteDue(ctx context.Context, limit int64) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	due, err := q.rdb.ZRangeByScore(ctx, retryKey, &redis.ZRangeBy{
		Min: "-inf", Max: fmt.Sprint(time.Now().UnixMilli()), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("jobs: read retry schedule: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}
	pipe := q.rdb.Pipeline()
	for _, sealed := range due {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: streamKey, Values: map[string]any{payloadField: sealed},
		})
		pipe.ZRem(ctx, retryKey, sealed)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("jobs: promote retries: %w", err)
	}
	return len(due), nil
}

// Stats summarizes queue depth for health reporting and metrics.
type Stats struct {
	StreamLength int64
	Pending      int64
	RetryDue     int64
	DeadLength   int64
}

func (q *Queue) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	pipe := q.rdb.Pipeline()
	streamLen := pipe.XLen(ctx, streamKey)
	pending := pipe.XPending(ctx, streamKey, groupName)
	due := pipe.ZCount(ctx, retryKey, "-inf", fmt.Sprint(time.Now().UnixMilli()))
	deadLen := pipe.XLen(ctx, deadKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return Stats{}, fmt.Errorf("jobs: queue stats: %w", err)
	}
	stats.StreamLength = streamLen.Val()
	stats.DeadLength = deadLen.Val()
	stats.RetryDue = due.Val()
	if summary, err := pending.Result(); err == nil {
		stats.Pending = summary.Count
	}
	return stats, nil
}

// backoff returns the delay before attempt n+1: exponential growth from the base
// delay, capped, with jitter so a burst of failing jobs does not retry in
// lockstep.
func (q *Queue) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := math.Min(float64(attempt-1), 32)
	delay := time.Duration(float64(q.baseDelay) * math.Pow(2, shift))
	if delay > q.maxDelay || delay <= 0 {
		delay = q.maxDelay
	}
	jitterMax := int64(delay / 2)
	if jitterMax <= 0 {
		return delay
	}
	jitter, err := rand.Int(rand.Reader, big.NewInt(jitterMax))
	if err != nil {
		return delay
	}
	return delay + time.Duration(jitter.Int64())
}

// PendingFor returns the stream ID awaiting acknowledgement, or an empty string.
// It exists for logging and tests.
func (q *Queue) PendingFor(ctx context.Context, jobID string) (string, error) {
	entries, err := q.rdb.XRange(ctx, streamKey, "-", "+").Result()
	if err != nil {
		return "", fmt.Errorf("jobs: scan stream: %w", err)
	}
	for _, entry := range entries {
		sealed, ok := entry.Values[payloadField].(string)
		if !ok {
			continue
		}
		if job, err := q.open(sealed); err == nil && job.ID == jobID {
			return entry.ID, nil
		}
	}
	return "", nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
