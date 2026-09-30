package ratelimit

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/observability"
)

// Response headers describing the caller's remaining budget. They are set on
// every limited response, allowed or not, so a client can slow down before it is
// rejected.
const (
	HeaderLimit      = "X-RateLimit-Limit"
	HeaderRemaining  = "X-RateLimit-Remaining"
	HeaderReset      = "X-RateLimit-Reset"
	HeaderRetryAfter = "Retry-After"
)

// Caller kinds, used for logging only.
const (
	CallerIP     = "ip"
	CallerUser   = "user"
	CallerAPIKey = "api_key"
)

// Middleware applies rate limits to requests.
type Middleware struct {
	limiter Limiter
	logger  *slog.Logger
	// failOpen allows requests through when the limiter itself is unavailable. A
	// Redis outage must not become an API outage, so the default is to allow and
	// log; it is a decision worth revisiting if abuse during an incident matters
	// more than availability.
	failOpen bool
	// now is overridable for deterministic tests.
	now func() time.Time
}

// NewMiddleware builds the rate limiting middleware.
func NewMiddleware(limiter Limiter, logger *slog.Logger, failOpen bool) *Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return &Middleware{limiter: limiter, logger: logger, failOpen: failOpen, now: time.Now}
}

// SetClock overrides the clock. It exists for tests.
func (m *Middleware) SetClock(now func() time.Time) { m.now = now }

// Limit applies a policy to requests identified by IP address. It is used on the
// unauthenticated surface, where no user or key exists yet.
func (m *Middleware) Limit(policy Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := httpx.ClientIP(r)
			if ip == "" {
				// Without any client address there is nothing meaningful to key
				// on; letting the request through is better than rejecting all of
				// them because a proxy is misconfigured.
				next.ServeHTTP(w, r)
				return
			}
			m.serve(w, r, next, policy, ip, CallerIP)
		})
	}
}

// LimitAuthenticated applies one policy to session traffic and another to API key
// traffic, so a machine credential cannot spend a user's budget.
func (m *Middleware) LimitAuthenticated(userPolicy, apiKeyPolicy Policy) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := authctx.PrincipalFromContext(r.Context())
			if !ok {
				// Unauthenticated requests are handled by Limit.
				next.ServeHTTP(w, r)
				return
			}
			if principal.IsAPIKey() {
				m.serve(w, r, next, apiKeyPolicy, principal.APIKeyID.String(), CallerAPIKey)
				return
			}
			m.serve(w, r, next, userPolicy, principal.UserID.String(), CallerUser)
		})
	}
}

// serve applies one policy and either continues or rejects the request.
func (m *Middleware) serve(w http.ResponseWriter, r *http.Request, next http.Handler,
	policy Policy, identifier, caller string,
) {
	decision, err := m.limiter.Allow(r.Context(), policy, identifier)
	if err != nil {
		m.logger.Error("rate limiter unavailable",
			slog.String("policy", policy.Name),
			slog.String("caller", caller),
			slog.String("request_id", requestID(r)),
			slog.Any("error", err),
		)
		if m.failOpen {
			next.ServeHTTP(w, r)
			return
		}
		httpx.WriteError(w, r, m.logger, httpx.ErrRateLimited)
		return
	}

	// Remaining is written before the handler runs so it describes the budget this
	// request left behind.
	w.Header().Set(HeaderLimit, strconv.Itoa(decision.Limit))
	w.Header().Set(HeaderRemaining, strconv.Itoa(decision.Remaining))
	w.Header().Set(HeaderReset, strconv.FormatInt(resetAt(m.now(), decision), 10))

	if decision.Allowed {
		next.ServeHTTP(w, r)
		return
	}

	retryAfter := int(decision.RetryAfter.Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set(HeaderRetryAfter, strconv.Itoa(retryAfter))
	observability.LoggerWithRequestID(r.Context(), m.logger).Warn("request rate limited",
		slog.String("policy", policy.Name),
		slog.String("caller", caller),
		slog.String("path", r.URL.Path),
		slog.Int("limit", decision.Limit),
		slog.Duration("retry_after", decision.RetryAfter),
	)
	httpx.WriteError(w, r, m.logger, httpx.ErrRateLimited)
}

// resetAt returns the Unix time at which a full token is expected again.
func resetAt(now time.Time, decision Decision) int64 {
	if decision.Allowed {
		return now.Unix()
	}
	return now.Add(decision.RetryAfter).Unix()
}

func requestID(r *http.Request) string {
	id, _ := observability.RequestIDFromContext(r.Context())
	return id
}

// NopMiddleware returns a middleware that never limits anything. It keeps the
// wiring identical whether or not rate limiting is enabled.
func NopMiddleware(logger *slog.Logger) *Middleware {
	return NewMiddleware(noopLimiter{}, logger, true)
}

type noopLimiter struct{}

func (noopLimiter) Allow(context.Context, Policy, string) (Decision, error) {
	return Decision{Allowed: true, Remaining: 0}, nil
}
