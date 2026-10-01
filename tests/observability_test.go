package organizations_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/api"
	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/metrics"
	"github.com/itsMinar/team-flow/internal/organizations"
)

// newObservabilityTestServer wires the real router with audit recording and metrics
// enabled, so the audit endpoint and the exposition endpoint are exercised as
// deployed.
func newObservabilityTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool, string) {
	t.Helper()
	appMetrics := metrics.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var srv *httptest.Server
	var pool *pgxpool.Pool
	var secret string
	srv, pool, _, secret = newAppTestServer(t, func(deps *api.Dependencies, opts *appServerOptions, testPool *pgxpool.Pool, appSecret string) {
		opts.metrics = appMetrics
		opts.auditRecorder = audit.NewSQLRecorder(db.New(testPool), appMetrics, logger)
		orgSvc := organizations.NewService(testPool, logger)
		opts.orgHandler = organizations.NewHandler(orgSvc, opts.auditRecorder, logger)
		opts.auditHandler = audit.NewHandler(audit.NewService(testPool, orgSvc), logger)
		opts.apiKeyService = apikeys.NewService(testPool, orgSvc, opts.auditRecorder,
			90*24*time.Hour, 365*24*time.Hour, logger)
	})
	return srv, pool, secret
}

func TestAuditEndpointOverHTTP(t *testing.T) {
	srv, pool, secret := newObservabilityTestServer(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	registered, err := auth.NewService(pool, auth.NewJWTService(secret, "teamflow", 15*time.Minute),
		720*time.Hour, audit.NopRecorder(), logger).Register(ctx, auth.RegisterInput{
		Email: "audit-http@example.com", Password: "StrongPassword123",
		FirstName: "Audit", LastName: "Http", OrganizationName: "Audit HTTP Org",
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
	base := "/api/v1/organizations/" + orgID.String()

	// The audit log requires authentication.
	if status, _ := doJSON(t, srv, http.MethodGet, base+"/audit-logs", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated audit read = %d, want 401", status)
	}

	// Minting a credential is recorded in the same transaction, and it shows up in
	// the organization's audit log.
	status, created := doJSON(t, srv, http.MethodPost, base+"/api-keys", token,
		`{"name":"audited key"}`)
	if status != http.StatusCreated {
		t.Fatalf("create api key = %d, %v", status, created)
	}

	// The owner may read it, and the envelope carries pagination metadata.
	status, body := doJSON(t, srv, http.MethodGet, base+"/audit-logs?page_size=5", token, "")
	if status != http.StatusOK {
		t.Fatalf("audit read = %d, %s", status, body)
	}
	pagination, ok := body["pagination"].(map[string]any)
	if !ok || pagination["page_size"].(float64) != 5 {
		t.Fatalf("unexpected pagination: %s", body)
	}
	entries, ok := body["data"].([]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("expected audit entries: %s", body)
	}
	first := entries[0].(map[string]any)
	for _, field := range []string{"id", "action", "outcome", "created_at", "metadata", "actor_user_id"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("entry is missing %q: %v", field, first)
		}
	}
	if first["action"] != audit.APIKeyCreated || first["outcome"] != audit.OutcomeSuccess {
		t.Fatalf("unexpected newest entry: %v", first)
	}

	// Filter validation is enforced at the HTTP boundary.
	for _, query := range []string{"outcome=maybe", "since=not-a-time", "actor_user_id=nope", "since=2000-01-01T00:00:00Z"} {
		status, body := doJSON(t, srv, http.MethodGet, base+"/audit-logs?"+query, token, "")
		if status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400 (%s)", query, status, body)
		}
	}
	status, body = doJSON(t, srv, http.MethodGet, base+"/audit-logs?action="+audit.APIKeyCreated, token, "")
	if status != http.StatusOK {
		t.Fatalf("action filter = %d, %s", status, body)
	}
	if filtered, ok := body["data"].([]any); !ok || len(filtered) != 1 {
		t.Fatalf("action filter returned %v", body)
	}
}

func TestMetricsEndpointAndTraceHeaders(t *testing.T) {
	srv, _, _ := newObservabilityTestServer(t)

	// A request carries a trace id, and an inbound traceparent is adopted.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	inbound := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req.Header.Set("traceparent", inbound)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Trace-Id"); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id header = %q, want the inbound trace id", got)
	}

	// A request without one gets a generated trace id.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/ready", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("X-Trace-Id"); len(got) != 32 {
		t.Fatalf("generated trace id = %q", got)
	}

	// Metrics are exposed in the Prometheus text format and describe the requests
	// this process actually served.
	status, exposition := rawJSON(t, srv, http.MethodGet, "/metrics", "", "")
	if status != http.StatusOK {
		t.Fatalf("/metrics = %d", status)
	}
	for _, want := range []string{
		"# HELP teamflow_http_requests_total",
		"# TYPE teamflow_http_requests_total counter",
		"teamflow_http_request_duration_seconds_bucket",
		"teamflow_http_requests_in_flight",
	} {
		if !strings.Contains(exposition, want) {
			t.Fatalf("exposition is missing %q", want)
		}
	}
	// Route labels use patterns, never raw paths with identifiers.
	if !strings.Contains(exposition, `route="/health"`) {
		t.Fatalf("expected a /health route label:\n%s", firstLines(exposition, 40))
	}
	if strings.Contains(exposition, `teamflow_http_requests_total{method="GET",route="/api/v1/organizations/`) {
		t.Fatal("route labels must not contain raw resource paths")
	}
}

func firstLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
