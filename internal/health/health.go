// Package health provides liveness and readiness HTTP handlers. Liveness
// reports whether the process is running; readiness verifies that critical
// dependencies (PostgreSQL, Redis) are reachable.
package health

import (
	"context"
	"net/http"

	"log/slog"

	"github.com/itsMinar/team-flow/internal/httpx"
)

// Checker verifies a single dependency is healthy.
type Checker interface {
	Health(ctx context.Context) error
}

// Handler serves health and readiness endpoints.
type Handler struct {
	logger *slog.Logger
	checks map[string]Checker
}

// NewHandler builds a health handler with the named dependency checks used by
// the readiness probe.
func NewHandler(logger *slog.Logger, checks map[string]Checker) *Handler {
	return &Handler{logger: logger, checks: checks}
}

// Live reports process liveness. It performs no dependency checks so a slow
// dependency never causes the orchestrator to kill an otherwise-healthy pod.
func (h *Handler) Live(w http.ResponseWriter, r *http.Request) {
	httpx.WriteData(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready verifies all critical dependencies are reachable. It returns 503 if any
// dependency is unavailable, without leaking infrastructure detail.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	results := make(map[string]string, len(h.checks))
	ok := true

	for name, check := range h.checks {
		if err := check.Health(r.Context()); err != nil {
			ok = false
			results[name] = "unavailable"
			h.logger.Warn("readiness check failed", slog.String("dependency", name), slog.Any("error", err))
			continue
		}
		results[name] = "ok"
	}

	status := http.StatusOK
	overall := "ok"
	if !ok {
		status = http.StatusServiceUnavailable
		overall = "unavailable"
	}

	httpx.WriteData(w, status, map[string]any{
		"status":       overall,
		"dependencies": results,
	})
}
