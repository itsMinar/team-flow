package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
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
	})
	orgID := uuid.NewString()
	for _, path := range []string{
		"/api/v1/organizations/" + orgID,
		"/api/v1/organizations/" + orgID + "/teams",
		"/api/v1/organizations/" + orgID + "/projects",
		"/api/v1/organizations/" + orgID + "/projects/" + uuid.NewString(),
		"/api/v1/organizations/" + orgID + "/projects/" + uuid.NewString() + "/activity",
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}
