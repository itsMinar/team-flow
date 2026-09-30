package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

func newTestRouter() http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20
	return NewRouter(Dependencies{
		Config: cfg,
		Logger: logger,
		Health: health.NewHandler(logger, map[string]health.Checker{}),
	})
}

func TestRouter_Health(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header on response")
	}
}

func TestRouter_Ready(t *testing.T) {
	srv := httptest.NewServer(newTestRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	defer resp.Body.Close()
	// No dependencies registered -> all pass -> 200.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRouter_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_SecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected security headers to be applied")
	}
}

// Nested /organizations/{orgID}/... modules must not be shadowed by the organizations router.
func TestRouter_TenantModulesRequireAuth(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20
	router := NewRouter(Dependencies{
		Config:       cfg,
		Logger:       logger,
		Health:       health.NewHandler(logger, map[string]health.Checker{}),
		AuthMW:       auth.NewMiddleware(auth.NewJWTService("router-test-secret", "teamflow", time.Minute), logger),
		OrgHandler:   organizations.NewHandler(nil, logger),
		OrgMW:        organizations.NewMiddleware(nil, logger),
		TeamsHandler: teams.NewHandler(nil, logger),
		Projects:     projects.NewHandler(nil, logger),
		Tasks:        tasks.NewHandler(nil, logger),
		Invitations:  invitations.NewHandler(nil, logger),
		APIKeys:      apikeys.NewHandler(nil, logger),
	})
	orgID := uuid.NewString()
	for _, path := range []string{
		"/api/v1/organizations/" + orgID,
		"/api/v1/organizations/" + orgID + "/teams",
		"/api/v1/organizations/" + orgID + "/projects",
		"/api/v1/organizations/" + orgID + "/projects/" + uuid.NewString(),
		"/api/v1/organizations/" + orgID + "/projects/" + uuid.NewString() + "/activity",
		"/api/v1/organizations/" + orgID + "/tasks",
		"/api/v1/organizations/" + orgID + "/tasks/" + uuid.NewString(),
		"/api/v1/organizations/" + orgID + "/tasks/" + uuid.NewString() + "/activity",
		"/api/v1/organizations/" + orgID + "/projects/" + uuid.NewString() + "/tasks",
		"/api/v1/organizations/" + orgID + "/invitations",
		"/api/v1/organizations/" + orgID + "/invitations/" + uuid.NewString() + "/resend",
		"/api/v1/organizations/" + orgID + "/invitations/" + uuid.NewString() + "/revoke",
		"/api/v1/organizations/" + orgID + "/api-keys",
		"/api/v1/organizations/" + orgID + "/api-keys/" + uuid.NewString(),
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}

// The invitation accept and preview endpoints are public: the token is the
// credential, so a missing Authorization header must not produce a 401.
func TestRouter_PublicInvitationRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20
	router := NewRouter(Dependencies{
		Config:      cfg,
		Logger:      logger,
		Health:      health.NewHandler(logger, map[string]health.Checker{}),
		AuthMW:      auth.NewMiddleware(auth.NewJWTService("router-test-secret", "teamflow", time.Minute), logger),
		Invitations: invitations.NewHandler(nil, logger),
	})
	for path, method := range map[string]string{
		"/api/v1/invitations/" + uuid.NewString(): http.MethodGet,
		"/api/v1/invitations/accept":              http.MethodPost,
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s must not require authentication", method, path)
		}
	}
}
