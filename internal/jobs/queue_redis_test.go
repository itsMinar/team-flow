package jobs

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisClientForTest connects to TEST_REDIS_URL when it is set. The queue tests
// are skipped otherwise, because they need a real Redis server.
func redisClientForTest(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL to a disposable Redis instance to run the queue tests")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	if opts.DB != 15 {
		t.Fatal("TEST_REDIS_URL must use Redis database 15; refusing to touch other databases")
	}
	opts.MaxRetries = -1
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect to TEST_REDIS_URL: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newRedisQueueForTest returns a queue plus a cleanup that removes only the keys
// this package owns.
func newRedisQueueForTest(t *testing.T, opts Options) *Queue {
	t.Helper()
	client := redisClientForTest(t)
	if opts.EncryptionKey == "" {
		opts.EncryptionKey = "test-key-material"
	}
	queue, err := NewQueue(client, opts)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	ctx := context.Background()
	for _, key := range []string{streamKey, retryKey, deadKey} {
		if err := client.Del(ctx, key).Err(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, key := range []string{streamKey, retryKey, deadKey} {
			_ = client.Del(context.Background(), key).Err()
		}
	})
	return queue
}

func TestQueueDeliversAndAcknowledges(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{MaxAttempts: 3})
	ctx := context.Background()
	if err := queue.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}

	enqueued, err := queue.Enqueue(ctx, "invitation.email", map[string]string{"token": "tfk_secret"})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if enqueued.ID == "" || enqueued.MaxAttempts != 3 {
		t.Fatalf("unexpected job: %+v", enqueued)
	}

	deliveries, err := queue.Read(ctx, "consumer-1", 1, time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Job.ID != enqueued.ID {
		t.Fatalf("unexpected deliveries: %+v", deliveries)
	}
	if !strings.Contains(string(deliveries[0].Job.Payload), "tfk_secret") {
		t.Fatalf("payload did not survive the round trip: %s", deliveries[0].Job.Payload)
	}
	if err := queue.Ack(ctx, deliveries[0]); err != nil {
		t.Fatalf("ack: %v", err)
	}

	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.StreamLength != 0 || stats.Pending != 0 {
		t.Fatalf("acknowledged work must leave the stream: %+v", stats)
	}
}

func TestQueueRetriesWithBackoffThenDeadLetters(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{MaxAttempts: 2, RetryBaseDelay: time.Hour, RetryMaxDelay: time.Hour})
	ctx := context.Background()
	if err := queue.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(ctx, "flaky", map[string]string{"n": "1"}); err != nil {
		t.Fatal(err)
	}

	first, err := queue.Read(ctx, "consumer-1", 1, time.Second)
	if err != nil || len(first) != 1 {
		t.Fatalf("read: %+v, %v", first, err)
	}
	if err := queue.Fail(ctx, first[0], errString("temporary")); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// The retry is held for an hour, so it is not claimable yet.
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RetryDue != 0 || stats.DeadLength != 0 {
		t.Fatalf("the retry must wait out its backoff: %+v", stats)
	}
	if promoted, err := queue.PromoteDue(ctx, 10); err != nil || promoted != 0 {
		t.Fatalf("promoted %d jobs before their backoff elapsed (%v)", promoted, err)
	}
	if deliveries, err := queue.Read(ctx, "consumer-1", 1, time.Second); err != nil || len(deliveries) != 0 {
		t.Fatalf("a delayed retry must not be claimable: %+v, %v", deliveries, err)
	}

	// Age the scheduled retry, promote it, and fail it again: the attempt budget
	// is spent, so it is dead-lettered instead of retried a third time.
	member := mustMember(t, queue)
	if err := queue.rdb.ZAdd(ctx, retryKey, redis.Z{
		Score: float64(time.Now().Add(-time.Minute).UnixMilli()), Member: member,
	}).Err(); err != nil {
		t.Fatal(err)
	}
	if promoted, err := queue.PromoteDue(ctx, 10); err != nil || promoted != 1 {
		t.Fatalf("promoted %d jobs, want 1 (%v)", promoted, err)
	}
	second, err := queue.Read(ctx, "consumer-1", 1, time.Second)
	if err != nil || len(second) != 1 {
		t.Fatalf("second read: %+v, %v", second, err)
	}
	if second[0].Job.Attempts != 1 {
		t.Fatalf("the retry must carry its attempt count: %+v", second[0].Job)
	}
	if err := queue.Fail(ctx, second[0], errString("still failing")); err != nil {
		t.Fatalf("second fail: %v", err)
	}
	stats, err = queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeadLength != 1 || stats.Pending != 0 {
		t.Fatalf("the exhausted job must be dead-lettered: %+v", stats)
	}
}

// mustMember returns one member currently held in the retry schedule.
func mustMember(t *testing.T, queue *Queue) string {
	t.Helper()
	members, err := queue.rdb.ZRange(context.Background(), retryKey, 0, -1).Result()
	if err != nil || len(members) == 0 {
		t.Fatalf("no retry scheduled: %v", err)
	}
	return members[0]
}

func TestQueueDeadLettersAfterMaxAttempts(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{MaxAttempts: 1})
	ctx := context.Background()
	if err := queue.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(ctx, "always-fails", nil); err != nil {
		t.Fatal(err)
	}
	delivery, err := queue.Read(ctx, "consumer-1", 1, time.Second)
	if err != nil || len(delivery) != 1 {
		t.Fatalf("read: %+v, %v", delivery, err)
	}
	if err := queue.Fail(ctx, delivery[0], Permanent(errString("gone"))); err != nil {
		t.Fatalf("fail: %v", err)
	}
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeadLength != 1 {
		t.Fatalf("the job must be dead-lettered: %+v", stats)
	}
	if stats.Pending != 0 {
		t.Fatalf("a dead-lettered job must leave the pending list: %+v", stats)
	}
}

func TestQueueDelaysAndPromotesJobs(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{})
	ctx := context.Background()
	if err := queue.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.EnqueueIn(ctx, time.Hour, "later", nil); err != nil {
		t.Fatal(err)
	}
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RetryDue != 0 || stats.StreamLength != 0 {
		t.Fatalf("a delayed job must not be claimable: %+v", stats)
	}
	if _, err := queue.EnqueueIn(ctx, time.Hour, "due", nil); err != nil {
		t.Fatal(err)
	}
	delayed, err := queue.rdb.ZRange(ctx, retryKey, 0, -1).Result()
	if err != nil || len(delayed) != 2 {
		t.Fatalf("scheduled jobs = %d, error = %v; want 2", len(delayed), err)
	}
	var dueJob string
	for _, member := range delayed {
		job, err := queue.open(member)
		if err != nil {
			t.Fatal(err)
		}
		if job.Type == "due" {
			dueJob = member
		}
	}
	if dueJob == "" {
		t.Fatal("scheduled due job was not found")
	}
	if err := queue.rdb.ZAdd(ctx, retryKey, redis.Z{
		Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: dueJob,
	}).Err(); err != nil {
		t.Fatal(err)
	}
	promoted, err := queue.PromoteDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if promoted != 1 {
		t.Fatalf("promoted %d jobs, want 1", promoted)
	}
	deliveries, err := queue.Read(ctx, "consumer-1", 10, time.Second)
	if err != nil || len(deliveries) != 1 || deliveries[0].Job.Type != "due" {
		t.Fatalf("unexpected deliveries: %+v, %v", deliveries, err)
	}
}

// A job left unacknowledged by a dead consumer is reclaimed rather than stranded.
func TestQueueRecoversStaleJobs(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{})
	ctx := context.Background()
	if err := queue.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(ctx, "stranded", nil); err != nil {
		t.Fatal(err)
	}
	// Read without acknowledging, simulating a consumer that died.
	if _, err := queue.Read(ctx, "dead-consumer", 1, time.Second); err != nil {
		t.Fatal(err)
	}
	stats, err := queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Fatalf("the job should be pending: %+v", stats)
	}
	// It is not stale yet.
	if recovered, err := queue.RecoverStale(ctx, "reclaimer", time.Hour, 10); err != nil || len(recovered) != 0 {
		t.Fatalf("a fresh claim must not be stolen: %+v, %v", recovered, err)
	}
	recovered, err := queue.RecoverStale(ctx, "reclaimer", time.Nanosecond, 10)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("a stale claim must be recovered: %+v, %v", recovered, err)
	}
	if err := queue.Ack(ctx, recovered[0]); err != nil {
		t.Fatal(err)
	}
}

// ensureGroup is idempotent, because every worker calls it on startup.
func TestEnsureGroupIsIdempotent(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := queue.EnsureGroup(ctx); err != nil {
			t.Fatalf("EnsureGroup call %d: %v", i, err)
		}
	}
}

func TestQueueRejectsAnEmptyJobType(t *testing.T) {
	queue := newRedisQueueForTest(t, Options{})
	if _, err := queue.Enqueue(context.Background(), "", nil); err == nil {
		t.Fatal("a job must have a type")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
