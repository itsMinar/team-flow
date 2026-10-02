package ratelimit

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisLimiterForTest builds a limiter against TEST_REDIS_URL. These tests are
// skipped without a Redis server because the token bucket is evaluated inside
// Redis and cannot be verified without one.
func redisLimiterForTest(t *testing.T) *RedisLimiter {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL to a disposable Redis instance to run the limiter tests")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	if opts.DB != 15 {
		t.Fatal("TEST_REDIS_URL must use Redis database 15; refusing to touch other databases")
	}
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect to TEST_REDIS_URL: %v", err)
	}
	limiter, err := NewRedisLimiter(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Only this package's keys are touched.
	t.Cleanup(func() {
		keys, err := client.Keys(ctx, keyPrefix+"*").Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
		_ = client.Close()
	})
	return limiter
}

func TestRedisBucketConsumesThenRefills(t *testing.T) {
	limiter := redisLimiterForTest(t)
	ctx := context.Background()
	policy := Policy{Name: "auth", Limit: 3, Period: time.Minute}
	identifier := "203.0.113.10"

	for i := 1; i <= 3; i++ {
		decision, err := limiter.Allow(ctx, policy, identifier)
		if err != nil {
			t.Fatalf("allow %d: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("request %d was rejected: %+v", i, decision)
		}
		if decision.Limit != 3 || decision.Remaining != 3-i {
			t.Fatalf("request %d reported %+v", i, decision)
		}
	}

	blocked, err := limiter.Allow(ctx, policy, identifier)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Allowed || blocked.RetryAfter <= 0 {
		t.Fatalf("an exhausted bucket must report a retry delay: %+v", blocked)
	}

	// The refill rate is derived from the limit and period, so moving the clock
	// forward makes budget available again.
	later := time.Now().Add(2 * policy.Period)
	limiter.SetClock(func() time.Time { return later })
	refilled, err := limiter.Allow(ctx, policy, identifier)
	if err != nil {
		t.Fatal(err)
	}
	if !refilled.Allowed || refilled.Remaining != 2 {
		t.Fatalf("after refilling the bucket should be full again: %+v", refilled)
	}
}

func TestRedisBucketsAreIsolated(t *testing.T) {
	limiter := redisLimiterForTest(t)
	ctx := context.Background()
	policy := Policy{Name: "user", Limit: 1, Period: time.Minute}

	if decision, err := limiter.Allow(ctx, policy, "user-a"); err != nil || !decision.Allowed {
		t.Fatalf("first caller: %+v %v", decision, err)
	}
	if decision, err := limiter.Allow(ctx, policy, "user-a"); err != nil || decision.Allowed {
		t.Fatalf("second request for the same caller must be blocked: %+v %v", decision, err)
	}
	if decision, err := limiter.Allow(ctx, policy, "user-b"); err != nil || !decision.Allowed {
		t.Fatalf("a different caller has its own budget: %+v %v", decision, err)
	}
	// A different policy is a different bucket even for the same caller.
	other := Policy{Name: "api_key", Limit: 1, Period: time.Minute}
	if decision, err := limiter.Allow(ctx, other, "user-a"); err != nil || !decision.Allowed {
		t.Fatalf("a different policy has its own budget: %+v %v", decision, err)
	}
}

// The bucket is evaluated inside Redis, so a concurrent burst cannot overspend
// the limit.
func TestRedisBucketIsAtomicUnderConcurrency(t *testing.T) {
	limiter := redisLimiterForTest(t)
	ctx := context.Background()
	policy := Policy{Name: "auth", Limit: 25, Period: time.Hour}
	identifier := "198.51.100.7"

	const callers = 60
	var wg sync.WaitGroup
	results := make([]bool, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			decision, err := limiter.Allow(ctx, policy, identifier)
			if err != nil {
				t.Errorf("allow: %v", err)
				return
			}
			results[i] = decision.Allowed
		}(i)
	}
	close(start)
	wg.Wait()

	var allowed int
	for _, ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != policy.Limit {
		t.Fatalf("allowed %d requests, want exactly the limit of %d", allowed, policy.Limit)
	}
}

func TestRedisBucketSetsAnExpiry(t *testing.T) {
	limiter := redisLimiterForTest(t)
	ctx := context.Background()
	policy := Policy{Name: "auth", Limit: 5, Period: time.Minute}
	if _, err := limiter.Allow(ctx, policy, "203.0.113.20"); err != nil {
		t.Fatal(err)
	}
	ttl, err := limiter.rdb.TTL(ctx, Key(policy, "203.0.113.20")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > policy.TTL() {
		t.Fatalf("ttl = %v, want a positive value up to %v", ttl, policy.TTL())
	}
}

func TestRedisLimiterRejectsInvalidPolicies(t *testing.T) {
	limiter := redisLimiterForTest(t)
	if _, err := limiter.Allow(context.Background(), Policy{Limit: 0, Period: time.Minute}, "x"); err == nil {
		t.Fatal("expected an invalid policy to be rejected before touching redis")
	}
}

func TestNewRedisLimiterRequiresRedis(t *testing.T) {
	if _, err := NewRedisLimiter(nil); err == nil {
		t.Fatal("a limiter requires a redis client")
	}
}

func TestRedisPayloadsNeverContainIdentifiers(t *testing.T) {
	limiter := redisLimiterForTest(t)
	ctx := context.Background()
	policy := Policy{Name: "auth", Limit: 2, Period: time.Minute}
	if _, err := limiter.Allow(ctx, policy, "invitee@example.com"); err != nil {
		t.Fatal(err)
	}
	entries, err := limiter.rdb.HGetAll(ctx, Key(policy, "invitee@example.com")).Result()
	if err != nil {
		t.Fatal(err)
	}
	for field, value := range entries {
		if strings.Contains(value, "invitee@example.com") {
			t.Fatalf("field %s leaked the identifier: %q", field, value)
		}
	}
}
