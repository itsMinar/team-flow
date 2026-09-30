// Package ratelimit implements Phase 11: Redis-backed request rate limiting.
//
// Limits are enforced with a token bucket evaluated by a Lua script, so the
// check and the refill happen atomically inside Redis. That matters more than the
// algorithm: with separate GET and SET calls, a burst of concurrent requests
// would all read the same balance and all succeed.
//
// Callers are identified by whatever the caller already is: an IP address for the
// unauthenticated surface, a user for session traffic, and an API key for
// machine traffic. Each has its own policy, so a leaked API key cannot consume a
// user's session budget, and neither can exhaust the other's.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces limiter state in Redis.
const keyPrefix = "teamflow:ratelimit:"

// Policy is a token bucket configuration: a burst capacity and a sustained refill
// rate derived from a limit per period.
type Policy struct {
	// Name identifies the policy in logs and metrics.
	Name string
	// Limit is how many requests are allowed per Period.
	Limit int
	// Period is the window the Limit applies to.
	Period time.Duration
}

// Capacity returns the burst capacity, which is the per-period limit.
func (p Policy) Capacity() int { return p.Limit }

// RefillPerSecond returns the sustained refill rate, in tokens per second.
func (p Policy) RefillPerSecond() float64 {
	if p.Period <= 0 {
		return 0
	}
	return float64(p.Limit) / p.Period.Seconds()
}

// TTL returns how long an idle bucket is kept. A bucket that has refilled to full
// carries no information, so it is only kept long enough to smooth the window.
func (p Policy) TTL() time.Duration {
	if p.Period <= 0 {
		return time.Minute
	}
	return 2 * p.Period
}

func (p Policy) validate() error {
	if p.Name == "" {
		return fmt.Errorf("ratelimit: policy %d/%s has no name", p.Limit, p.Period)
	}
	if p.Limit < 1 {
		return fmt.Errorf("ratelimit: policy %s must allow at least one request", p.Name)
	}
	if p.Period <= 0 {
		return fmt.Errorf("ratelimit: policy %s must have a positive period", p.Name)
	}
	return nil
}

// Decision is the outcome of a limit check.
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool
	// Limit is the burst capacity, echoed in X-RateLimit-Limit.
	Limit int
	// Remaining is the whole tokens left in the bucket.
	Remaining int
	// RetryAfter is how long until one token is available. Zero when allowed.
	RetryAfter time.Duration
}

// Limiter checks and consumes rate limit budget. It is an interface so the
// middleware can be exercised without Redis.
type Limiter interface {
	Allow(ctx context.Context, policy Policy, key string) (Decision, error)
}

// bucketScript refills and consumes one token atomically. It returns
// {allowed, remaining_tokens, retry_after_ms}.
var bucketScript = redis.NewScript(`
local capacity = tonumber(ARGV[1])
local refill = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts = tonumber(state[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

local elapsed = now - ts
if elapsed < 0 then elapsed = 0 end
tokens = math.min(capacity, tokens + (elapsed / 1000.0) * refill)

local allowed = 0
local retry_after_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_after_ms = math.ceil(((1 - tokens) / refill) * 1000)
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', KEYS[1], ttl)

return {allowed, math.floor(tokens), retry_after_ms}
`)

// RedisLimiter enforces policies in Redis.
type RedisLimiter struct {
	rdb redis.UniversalClient
	// now is overridable for deterministic tests.
	now func() time.Time
}

// NewRedisLimiter builds a limiter backed by Redis.
func NewRedisLimiter(rdb redis.UniversalClient) (*RedisLimiter, error) {
	if rdb == nil {
		return nil, fmt.Errorf("ratelimit: a redis client is required")
	}
	return &RedisLimiter{rdb: rdb, now: time.Now}, nil
}

// SetClock overrides the clock. It exists for tests.
func (l *RedisLimiter) SetClock(now func() time.Time) { l.now = now }

// Key builds the Redis key for a policy and caller. Identifiers are hashed so the
// key space never contains an email address or a user id in the clear.
func Key(policy Policy, identifier string) string {
	sum := sha256.Sum256([]byte(identifier))
	return keyPrefix + policy.Name + ":" + hex.EncodeToString(sum[:16])
}

// Allow consumes one token for the caller, refilling the bucket first.
func (l *RedisLimiter) Allow(ctx context.Context, policy Policy, identifier string) (Decision, error) {
	if err := policy.validate(); err != nil {
		return Decision{}, err
	}
	now := l.now()
	result, err := bucketScript.Run(ctx, l.rdb,
		[]string{Key(policy, identifier)},
		policy.Capacity(), policy.RefillPerSecond(), now.UnixMilli(), policy.TTL().Milliseconds(),
	).Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("ratelimit: evaluate bucket: %w", err)
	}
	if len(result) != 3 {
		return Decision{}, fmt.Errorf("ratelimit: unexpected script result %v", result)
	}
	allowed, _ := toInt64(result[0])
	remaining, _ := toInt64(result[1])
	retryMS, _ := toInt64(result[2])
	decision := Decision{
		Allowed: allowed == 1, Limit: policy.Capacity(), Remaining: int(remaining),
	}
	if !decision.Allowed && retryMS > 0 {
		decision.RetryAfter = time.Duration(retryMS) * time.Millisecond
	}
	if decision.Remaining < 0 {
		decision.Remaining = 0
	}
	return decision, nil
}

func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		return 0, fmt.Errorf("ratelimit: unexpected string result %q", typed)
	default:
		return 0, fmt.Errorf("ratelimit: unexpected result type %T", value)
	}
}

// Policies are the three per-surface limits the router applies. They are grouped
// so a feature module cannot pick a limit by accident: the router chooses the
// surface, and the surface chooses the bucket.
type Policies struct {
	// Auth applies to unauthenticated endpoints, keyed by client IP.
	Auth Policy
	// User applies to session traffic, keyed by user.
	User Policy
	// APIKey applies to machine traffic, keyed by API key.
	APIKey Policy
}
