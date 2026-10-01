// Package api wires together middleware, routes, and handlers into the HTTP
// application served by cmd/api. Later phases register feature routers here.
package api

import (
	"net/http"

	"log/slog"

	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/metrics"
	"github.com/itsMinar/team-flow/internal/middleware"
	"github.com/itsMinar/team-flow/internal/observability"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/ratelimit"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

// Dependencies holds everything the router needs. Dependencies are injected so
// the router has no hidden global state and is easy to test.
type Dependencies struct {
	Config       *config.Config
	Logger       *slog.Logger
	Health       *health.Handler
	AuthHandler  *auth.Handler
	AuthMW       *auth.Middleware
	OrgHandler   *organizations.Handler
	OrgMW        *organizations.Middleware
	TeamsHandler *teams.Handler
	Projects     *projects.Handler
	Tasks        *tasks.Handler
	Invitations  *invitations.Handler
	APIKeys      *apikeys.Handler
	AuditHandler *audit.Handler
	// RateLimit applies limits and RateLimits describes how much. Both are
	// optional: without them the router applies no limits, which keeps tests and
	// local runs unaffected by configuration.
	RateLimit  *ratelimit.Middleware
	RateLimits ratelimit.Policies
	// Metrics is optional; without it no metrics are collected or served.
	Metrics *metrics.Metrics
}

// NewRouter builds the top-level HTTP handler with the standard middleware
// chain applied in order:
//
//	Recovery -> RequestID -> Logging -> SecurityHeaders -> MaxBodyBytes
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Recovery(deps.Logger))
	r.Use(middleware.RequestID)
	r.Use(observability.TraceMiddleware)
	r.Use(middleware.Logging(deps.Logger))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.MaxBodyBytes(deps.Config.HTTP.MaxBodyBytes))
	if deps.Metrics != nil {
		// Metrics run inside the request ID and trace context so their labels are
		// collected on the same request the log lines describe.
		r.Use(deps.Metrics.Middleware)
	}

	// Operational endpoints live outside the versioned API surface.
	r.Get("/health", deps.Health.Live)
	r.Get("/ready", deps.Health.Ready)
	if deps.Metrics != nil {
		// The exposition endpoint is intentionally unauthenticated: Prometheus
		// scrapes it, and it exposes no request data. A deployment must restrict
		// it at the edge so metric names are not public.
		r.Method(http.MethodGet, "/metrics", deps.Metrics.Handler())
	}

	limits := deps.RateLimit
	if limits == nil {
		limits = ratelimit.NopMiddleware(deps.Logger)
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Authentication endpoints are keyed by client IP: no user exists yet to
		// key on, and this is the surface a credential-guessing attack hits first.
		if deps.AuthHandler != nil {
			r.Group(func(r chi.Router) {
				r.Use(limits.Limit(deps.RateLimits.Auth))
				deps.AuthHandler.RegisterRoutes(r, deps.AuthMW)
			})
		}

		// The invitation accept flow is public, keyed by client IP like the
		// authentication endpoints.
		if deps.Invitations != nil && deps.AuthMW != nil {
			r.Group(func(r chi.Router) {
				r.Use(limits.Limit(deps.RateLimits.Auth))
				deps.Invitations.RegisterPublicRoutes(r, deps.AuthMW)
			})
		}

		// Every tenant-scoped module shares one authenticated and rate limited
		// group, so a new feature module is covered by default instead of having to
		// remember to add a limiter.
		if deps.AuthMW != nil {
			r.Group(func(r chi.Router) {
				r.Use(deps.AuthMW.RequireAuth)
				r.Use(limits.LimitAuthenticated(deps.RateLimits.User, deps.RateLimits.APIKey))
				mountTenantRoutes(r, deps)
			})
		}
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

// mountTenantRoutes registers every organization-scoped feature module on an
// already authenticated and rate limited router.
func mountTenantRoutes(r chi.Router, deps Dependencies) {
	if deps.AuthMW == nil {
		return
	}
	if deps.OrgHandler != nil && deps.OrgMW != nil {
		deps.OrgHandler.RegisterRoutes(r, deps.AuthMW, deps.OrgMW)
	}
	if deps.TeamsHandler != nil {
		deps.TeamsHandler.RegisterRoutes(r, deps.AuthMW)
	}
	if deps.Projects != nil {
		deps.Projects.RegisterRoutes(r, deps.AuthMW)
	}
	if deps.Tasks != nil {
		deps.Tasks.RegisterRoutes(r, deps.AuthMW)
	}
	if deps.Invitations != nil {
		deps.Invitations.RegisterRoutes(r, deps.AuthMW)
	}
	if deps.APIKeys != nil {
		deps.APIKeys.RegisterRoutes(r, deps.AuthMW)
	}
	// The audit log is mounted here rather than by its own package so this
	// package stays the single place that decides what authentication a route
	// requires.
	if deps.AuditHandler != nil {
		r.Route("/organizations/{orgID}/audit-logs", func(r chi.Router) {
			r.Get("/", deps.AuditHandler.List)
		})
	}
}
