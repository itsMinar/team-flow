package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubChecker struct{ err error }

func (s stubChecker) Health(context.Context) error { return s.err }

func newTestHandler(checks map[string]Checker) *Handler {
	return NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), checks)
}

func TestLive_AlwaysOK(t *testing.T) {
	h := newTestHandler(nil)
	rec := httptest.NewRecorder()
	h.Live(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestReady_AllHealthy(t *testing.T) {
	h := newTestHandler(map[string]Checker{
		"postgres": stubChecker{nil},
		"redis":    stubChecker{nil},
	})
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestReady_DependencyDown(t *testing.T) {
	h := newTestHandler(map[string]Checker{
		"postgres": stubChecker{nil},
		"redis":    stubChecker{errors.New("connection refused")},
	})
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var body struct {
		Data struct {
			Status       string            `json:"status"`
			Dependencies map[string]string `json:"dependencies"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Data.Status != "unavailable" {
		t.Errorf("status = %q, want unavailable", body.Data.Status)
	}
	if body.Data.Dependencies["redis"] != "unavailable" {
		t.Errorf("redis = %q, want unavailable", body.Data.Dependencies["redis"])
	}
}
