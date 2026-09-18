// Package api wires together middleware, routes, and handlers into the HTTP
// application served by cmd/api. Later phases register feature routers here.
package api

import (
	"net/http"

	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/middleware"
)

// Dependencies holds everything the router needs. Dependencies are injected so
// the router has no hidden global state and is easy to test.
type Dependencies struct {
	Config *config.Config
	Logger *slog.Logger
	Health *health.Handler
}

// NewRouter builds the top-level HTTP handler with the standard middleware
// chain applied in order:
//
//	Recovery -> RequestID -> Logging -> SecurityHeaders -> MaxBodyBytes
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Recovery(deps.Logger))
	r.Use(middleware.RequestID)
	r.Use(middleware.Logging(deps.Logger))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.MaxBodyBytes(deps.Config.HTTP.MaxBodyBytes))

	// Operational endpoints live outside the versioned API surface.
	r.Get("/health", deps.Health.Live)
	r.Get("/ready", deps.Health.Ready)

	r.Route("/api/v1", func(r chi.Router) {
		// Feature routers are mounted here in subsequent phases
		// (auth, organizations, teams, projects, tasks, ...).
	})

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, deps.Logger, httpx.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteError(w, req, deps.Logger, httpx.NewAPIError(
			http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", nil))
	})

	return r
}
