package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// stubLimiter records calls and replays a scripted decision.
type stubLimiter struct {
	err       error
	decision  Decision
	calls     []stubCall
	allowedNs int
}

type stubCall struct {
	Policy     Policy
	Identifier string
}

func (s *stubLimiter) Allow(_ context.Context, policy Policy, identifier string) (Decision, error) {
	s.calls = append(s.calls, stubCall{Policy: policy, Identifier: identifier})
	if s.err != nil {
		return Decision{}, s.err
	}
	s.allowedNs++
	decision := s.decision
	if decision.Limit == 0 {
		decision.Limit = policy.Limit
	}
	decision.Remaining = policy.Limit - s.allowedNs
	if decision.Remaining < 0 {
		decision.Remaining = 0
	}
	decision.Allowed = s.allowedNs <= policy.Limit
	if !decision.Allowed && decision.RetryAfter == 0 {
		decision.RetryAfter = 30 * time.Second
	}
	return decision, nil
}

func (s *stubLimiter) lastCall() stubCall {
	if len(s.calls) == 0 {
		return stubCall{}
	}
	return s.calls[len(s.calls)-1]
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPolicyDerivation(t *testing.T) {
	policy := Policy{Name: "auth", Limit: 10, Period: time.Minute}
	if policy.Capacity() != 10 {
		t.Fatalf("capacity = %d", policy.Capacity())
	}
	if got := policy.RefillPerSecond(); got < 0.16 || got > 0.17 {
		t.Fatalf("refill = %v tokens/second, want about 0.167", got)
	}
	if policy.TTL() != 2*time.Minute {
		t.Fatalf("ttl = %v, want two periods", policy.TTL())
	}
	// A zero period must not divide by zero.
	broken := Policy{Name: "broken", Limit: 1}
	if got := broken.RefillPerSecond(); got != 0 {
		t.Fatalf("refill = %v, want 0", got)
	}
	if err := broken.validate(); err == nil {
		t.Fatal("a policy without a period must be rejected")
	}
	if err := (Policy{Name: "zero", Limit: 0, Period: time.Minute}).validate(); err == nil {
		t.Fatal("a policy allowing no requests must be rejected")
	}
	if err := (Policy{Limit: 1, Period: time.Minute}).validate(); err == nil {
		t.Fatal("a policy without a name must be rejected")
	}
}

func TestKeyIsNamespacedAndHashed(t *testing.T) {
	policy := Policy{Name: "auth", Limit: 10, Period: time.Minute}
	key := Key(policy, "203.0.113.7")
	if !strings.HasPrefix(key, keyPrefix+"auth:") {
		t.Fatalf("key %q is not namespaced by policy", key)
	}
	if strings.Contains(key, "203.0.113.7") {
		t.Fatalf("key %q must not contain the identifier", key)
	}
	if Key(policy, "203.0.113.7") != key {
		t.Fatal("keying must be deterministic")
	}
	if Key(policy, "203.0.113.8") == key {
		t.Fatal("different callers must not share a bucket")
	}
	other := Policy{Name: "user", Limit: 10, Period: time.Minute}
	if Key(other, "203.0.113.7") == key {
		t.Fatal("different policies must not share a bucket")
	}
}

func TestLimitByIPSetsHeadersAndRejects(t *testing.T) {
	limiter := &stubLimiter{}
	mw := NewMiddleware(limiter, testLogger(), true)
	policy := Policy{Name: "auth", Limit: 2, Period: time.Minute}
	handler := mw.Limit(policy)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Allowed requests still carry the remaining budget.
	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = "203.0.113.7:51234"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, rec.Code)
		}
		if rec.Header().Get(HeaderLimit) != "2" {
			t.Fatalf("limit header = %q", rec.Header().Get(HeaderLimit))
		}
		if got := rec.Header().Get(HeaderRemaining); got != "1" && got != "0" {
			t.Fatalf("remaining header = %q", got)
		}
		if rec.Header().Get(HeaderReset) == "" {
			t.Fatal("every limited response must carry a reset time")
		}
	}
	if limiter.lastCall().Identifier != "203.0.113.7" {
		t.Fatalf("identifier = %q, want the bare IP", limiter.lastCall().Identifier)
	}

	// The next request is rejected with the standard envelope.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.7:51234"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get(HeaderRetryAfter) != "30" {
		t.Fatalf("retry-after = %q, want 30", rec.Header().Get(HeaderRetryAfter))
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Error.Code != "RATE_LIMITED" || body.Error.Message == "" {
		t.Fatalf("unexpected error envelope: %+v", body.Error)
	}
}

func TestLimitByIPReadsTheForwardedAddress(t *testing.T) {
	limiter := &stubLimiter{}
	mw := NewMiddleware(limiter, testLogger(), true)
	handler := mw.Limit(Policy{Name: "auth", Limit: 5, Period: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1")
	handler.ServeHTTP(rec, req)
	if got := limiter.lastCall().Identifier; got != "198.51.100.9" {
		t.Fatalf("identifier = %q, want the first forwarded address", got)
	}
}

// A request with no usable client address is not rejected, because that would
// take the whole surface down behind a misconfigured proxy.
func TestLimitSkipsRequestsWithoutAnAddress(t *testing.T) {
	limiter := &stubLimiter{}
	mw := NewMiddleware(limiter, testLogger(), true)
	handler := mw.Limit(Policy{Name: "auth", Limit: 1, Period: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := authctx.PrincipalFromContext(r.Context()); ok {
				t.Error("no principal expected")
			}
			w.WriteHeader(http.StatusOK)
		}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = ""
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(limiter.calls) != 0 {
		t.Fatal("a request without an address must not consume budget")
	}
}

func TestLimitAuthenticatedUsesTheCallersBucket(t *testing.T) {
	userPolicy := Policy{Name: "user", Limit: 100, Period: time.Minute}
	keyPolicy := Policy{Name: "api_key", Limit: 1000, Period: time.Minute}
	limiter := &stubLimiter{}
	mw := NewMiddleware(limiter, testLogger(), true)
	handler := mw.LimitAuthenticated(userPolicy, keyPolicy)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	// Session traffic is keyed by user.
	userID, keyID := uuid.New(), uuid.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/x/projects", nil)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: userID, Method: authctx.MethodBearer,
	}))
	handler.ServeHTTP(rec, req)
	if call := limiter.lastCall(); call.Policy.Name != "user" || call.Identifier != userID.String() {
		t.Fatalf("unexpected call: %+v", call)
	}

	// Machine traffic gets its own, separate budget.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/organizations/x/projects", nil)
	req = req.WithContext(authctx.WithPrincipal(req.Context(), authctx.Principal{
		UserID: userID, APIKeyID: keyID, OrganizationID: uuid.New(), Method: authctx.MethodAPIKey,
	}))
	handler.ServeHTTP(rec, req)
	if call := limiter.lastCall(); call.Policy.Name != "api_key" || call.Identifier != keyID.String() {
		t.Fatalf("unexpected call: %+v", call)
	}
}

// Without a principal the middleware passes through: unauthenticated traffic is
// limited by the IP policy instead.
func TestLimitAuthenticatedIgnoresAnonymousRequests(t *testing.T) {
	limiter := &stubLimiter{}
	mw := NewMiddleware(limiter, testLogger(), true)
	handler := mw.LimitAuthenticated(
		Policy{Name: "user", Limit: 5, Period: time.Minute},
		Policy{Name: "api_key", Limit: 5, Period: time.Minute},
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/organizations/x/projects", nil))
	if rec.Code != http.StatusOK || len(limiter.calls) != 0 {
		t.Fatalf("status = %d, calls = %d", rec.Code, len(limiter.calls))
	}
}

func TestLimiterOutageFailsOpen(t *testing.T) {
	limiter := &stubLimiter{err: errors.New("dial tcp: connect: connection refused")}
	mw := NewMiddleware(limiter, testLogger(), true)
	handler := mw.Limit(Policy{Name: "auth", Limit: 1, Period: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.7:1"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a limiter outage must not take the API down: status = %d", rec.Code)
	}
}

func TestLimiterOutageFailsClosed(t *testing.T) {
	limiter := &stubLimiter{err: errors.New("dial tcp: connect: connection refused")}
	mw := NewMiddleware(limiter, testLogger(), false)
	handler := mw.Limit(Policy{Name: "auth", Limit: 1, Period: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("handler must not run") }))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "203.0.113.7:1"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestNopMiddlewareNeverLimits(t *testing.T) {
	mw := NopMiddleware(testLogger())
	handler := mw.Limit(Policy{Name: "auth", Limit: 1, Period: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = "203.0.113.7:1"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, rec.Code)
		}
	}
}

func TestRateLimitedErrorMapsToTooManyRequests(t *testing.T) {
	apiErr := httpx.FromError(httpx.ErrRateLimited)
	if apiErr.Status != http.StatusTooManyRequests || apiErr.Code != "RATE_LIMITED" {
		t.Fatalf("unexpected mapping: %+v", apiErr)
	}
}
