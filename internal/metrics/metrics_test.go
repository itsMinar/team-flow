package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestMiddlewareRecordsRequestsByRoutePattern(t *testing.T) {
	m := New()
	router := chi.NewRouter()
	router.Use(m.Middleware)
	router.Get("/organizations/{orgID}/projects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	router.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/organizations/abc-123/projects", nil))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	// A route that does not match still gets counted, under one bounded label.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	body := scrape(t, m)
	// The route label is the pattern, so two different IDs share one series.
	if !strings.Contains(body, `teamflow_http_requests_total{method="GET",route="/organizations/{orgID}/projects",status="2xx"} 3`) {
		t.Fatalf("expected the counted series:\n%s", body)
	}
	if strings.Contains(body, "abc-123") {
		t.Fatalf("route labels must not contain resource identifiers:\n%s", body)
	}
	if !strings.Contains(body, `route="unmatched"`) {
		t.Fatalf("unmatched requests must be counted under a bounded label:\n%s", body)
	}
	if !strings.Contains(body, "teamflow_http_request_duration_seconds_bucket") {
		t.Fatalf("latency histogram missing:\n%s", body)
	}
	if !strings.Contains(body, "teamflow_go_goroutines") && !strings.Contains(body, "go_goroutines") {
		t.Fatalf("runtime metrics missing:\n%s", body)
	}
}

func TestJobAndAuditMetricsAreExported(t *testing.T) {
	m := New()
	m.CountAudit("apikey.created", "success")
	m.CountJob("invitation.email", "completed")
	m.ObserveJob("invitation.email", 25*time.Millisecond)
	m.SetQueueDepth("dead", 3)

	body := scrape(t, m)
	for _, want := range []string{
		`teamflow_audit_events_total{action="apikey.created",outcome="success"} 1`,
		`teamflow_jobs_processed_total{result="completed",type="invitation.email"} 1`,
		"teamflow_jobs_duration_seconds_count",
		`teamflow_jobs_queue_depth{queue="dead"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in exposition:\n%s", want, body)
		}
	}
}

func TestMiddlewarePreservesFlushing(t *testing.T) {
	m := New()
	flushed := false
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		flushed = true
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !flushed {
		t.Fatal("handler did not run")
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content type = %q", ct)
	}
	return rec.Body.String()
}
