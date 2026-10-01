package organizations_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsMinar/team-flow/internal/api"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/jobs"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/ratelimit"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

// fixedLimiter answers every request with a scripted decision so the router's
// wiring can be tested without Redis.
type fixedLimiter struct {
	mu       sync.Mutex
	decision ratelimit.Decision
	err      error
	seen     []string
}

func (l *fixedLimiter) Allow(_ context.Context, policy ratelimit.Policy, identifier string) (ratelimit.Decision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, policy.Name+":"+identifier)
	if l.err != nil {
		return ratelimit.Decision{}, l.err
	}
	return l.decision, nil
}

func (l *fixedLimiter) policies() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.seen))
	for _, entry := range l.seen {
		out = append(out, strings.SplitN(entry, ":", 2)[0])
	}
	return out
}

func (l *fixedLimiter) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = nil
}

// newRateLimitedTestServer builds the real router with rate limiting enabled and
// a scripted limiter.
func newRateLimitedTestServer(t *testing.T, limiter *fixedLimiter) (*httptest.Server, *pgxpool.Pool, *auth.JWTService) {
	t.Helper()
	pool := testPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	secret := "http-test-secret"
	jwt := auth.NewJWTService(secret, "teamflow", 15*time.Minute)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, audit.NopRecorder(), logger)
	orgSvc := organizations.NewService(pool, logger)

	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20
	cfg.Invite.BaseURL = "http://app.example.com"
	cfg.Invite.TTL = 7 * 24 * time.Hour
	cfg.APIKey.DefaultTTL, cfg.APIKey.MaxTTL = 90*24*time.Hour, 365*24*time.Hour

	apiKeySvc := apikeys.NewService(pool, orgSvc, audit.NopRecorder(), cfg.APIKey.DefaultTTL, cfg.APIKey.MaxTTL, logger)
	authMW := auth.NewMiddleware(jwt, logger).WithAPIKeys(apiKeySvc)
	rateLimitMW := ratelimit.NewMiddleware(limiter, logger, true)

	router := api.NewRouter(api.Dependencies{
		Config:       cfg,
		Logger:       logger,
		Health:       health.NewHandler(logger, map[string]health.Checker{}),
		AuthHandler:  auth.NewHandler(authSvc, logger),
		AuthMW:       authMW,
		OrgHandler:   organizations.NewHandler(orgSvc, audit.NopRecorder(), logger),
		OrgMW:        organizations.NewMiddleware(orgSvc, logger),
		TeamsHandler: teams.NewHandler(teams.NewService(pool, orgSvc, logger), logger),
		Projects:     projects.NewHandler(projects.NewService(pool, orgSvc), logger),
		Tasks:        tasks.NewHandler(tasks.NewService(pool, orgSvc), logger),
		Invitations: invitations.NewHandler(
			invitations.NewService(pool, orgSvc, authSvc, &recordingSender{}, jobs.Enqueuer(nil), audit.NopRecorder(), cfg.Invite.TTL, cfg.Invite.BaseURL, logger), logger),
		APIKeys:   apikeys.NewHandler(apiKeySvc, logger),
		RateLimit: rateLimitMW,
		RateLimits: ratelimit.Policies{
			Auth:   ratelimit.Policy{Name: "auth", Limit: 10, Period: time.Minute},
			User:   ratelimit.Policy{Name: "user", Limit: 300, Period: time.Minute},
			APIKey: ratelimit.Policy{Name: "api_key", Limit: 600, Period: time.Minute},
		},
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, pool, jwt
}

func TestRateLimitingOverHTTP(t *testing.T) {
	limiter := &fixedLimiter{decision: ratelimit.Decision{Allowed: true, Limit: 10, Remaining: 9}}
	srv, pool, jwt := newRateLimitedTestServer(t, limiter)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, audit.NopRecorder(), logger)

	registered, err := authSvc.Register(ctx, auth.RegisterInput{
		Email: "limited@example.com", Password: "StrongPassword123",
		FirstName: "Rate", LastName: "Limited", OrganizationName: "Rate Limited Org",
	}, auth.RequestMeta{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token := registered.Tokens.AccessToken
	orgSvc := organizations.NewService(pool, logger)
	mine, err := orgSvc.ListMyOrganizations(ctx, registered.User.ID)
	if err != nil || len(mine) == 0 {
		t.Fatalf("list organizations: %v", err)
	}
	orgID := mine[0].ID

	t.Run("login is limited by client IP", func(t *testing.T) {
		limiter.reset()
		status, _ := postJSON(t, srv, "/api/v1/auth/login",
			`{"email":"limited@example.com","password":"StrongPassword123"}`, "")
		if status != http.StatusOK && status != http.StatusUnauthorized {
			t.Fatalf("login = %d", status)
		}
		policies := limiter.policies()
		if len(policies) != 1 || policies[0] != "auth" {
			t.Fatalf("login must use the auth policy, got %v", policies)
		}
	})

	t.Run("the public invitation accept route stays reachable but limited by IP", func(t *testing.T) {
		limiter.reset()
		status, body := postJSON(t, srv, "/api/v1/invitations/accept", `{"token":"nope"}`, "")
		if status == http.StatusUnauthorized || status == http.StatusTooManyRequests {
			t.Fatalf("accept must not require a session: %d %s", status, body)
		}
		policies := limiter.policies()
		if len(policies) != 1 || policies[0] != "auth" {
			t.Fatalf("accept must use the auth policy, got %v", policies)
		}
	})

	t.Run("tenant routes are limited per user", func(t *testing.T) {
		limiter.reset()
		status, body := doJSON(t, srv, http.MethodGet,
			"/api/v1/organizations/"+orgID.String()+"/projects", token, "")
		if status != http.StatusOK {
			t.Fatalf("list projects = %d, %s", status, body)
		}
		policies := limiter.policies()
		if len(policies) != 1 || policies[0] != "user" {
			t.Fatalf("tenant traffic must use the user policy, got %v", policies)
		}
		// Unauthenticated tenant traffic is rejected before any limit is consumed.
		limiter.reset()
		if status, _ := doJSON(t, srv, http.MethodGet,
			"/api/v1/organizations/"+orgID.String()+"/projects", "", ""); status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated request = %d, want 401", status)
		}
		if len(limiter.policies()) != 0 {
			t.Fatal("a rejected request must not consume budget")
		}
	})

	t.Run("a rejected request returns 429 with a retry hint", func(t *testing.T) {
		limiter.mu.Lock()
		limiter.decision = ratelimit.Decision{Allowed: false, Limit: 10, Remaining: 0, RetryAfter: 42 * time.Second}
		limiter.mu.Unlock()
		defer func() {
			limiter.mu.Lock()
			limiter.decision = ratelimit.Decision{Allowed: true, Limit: 10, Remaining: 9}
			limiter.mu.Unlock()
		}()

		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/organizations/"+orgID.String()+"/projects", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", resp.StatusCode)
		}
		if got := resp.Header.Get("Retry-After"); got != "42" {
			t.Fatalf("retry-after = %q", got)
		}
		if got := resp.Header.Get("X-RateLimit-Limit"); got != "10" {
			t.Fatalf("limit header = %q", got)
		}
		var payload struct {
			Error struct {
				Code      string `json:"code"`
				RequestID string `json:"request_id"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error.Code != "RATE_LIMITED" || payload.Error.RequestID == "" {
			t.Fatalf("unexpected error envelope: %+v", payload.Error)
		}
	})
}

// postJSON issues a public POST with a JSON body and returns the raw response.
func postJSON(t *testing.T, srv *httptest.Server, path, body, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}
