package organizations

import (
	"net/http"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// Middleware resolves the tenant from the {orgID} URL parameter.
type Middleware struct {
	service *Service
	logger  *slog.Logger
}

// NewMiddleware constructs the tenant Middleware.
func NewMiddleware(service *Service, logger *slog.Logger) *Middleware {
	return &Middleware{service: service, logger: logger}
}

// RequireTenant verifies the caller belongs to the organization in {orgID} and
// stores the resolved Tenant in context. Unknown orgs and non-members get 404
// to avoid leaking cross-tenant existence.
func (m *Middleware) RequireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := authctx.PrincipalFromContext(r.Context())
		if !ok {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized)
			return
		}
		orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
		if err != nil {
			httpx.WriteError(w, r, m.logger, httpx.ErrNotFound)
			return
		}
		tenant, err := m.service.ResolveTenant(r.Context(), principal.UserID, orgID)
		if err != nil {
			httpx.WriteError(w, r, m.logger, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(authctx.WithTenant(r.Context(), tenant)))
	})
}

// RegisterRoutes mounts organization endpoints.
func (h *Handler) RegisterRoutes(r chi.Router, authMW *auth.Middleware, tenantMW *Middleware) {
	r.Route("/organizations", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.ListMine)
		r.Post("/", h.Create)

		r.Route("/{orgID}", func(r chi.Router) {
			// Get/Update resolve tenant inside the service (service-level authz).
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Get("/roles", h.ListRoles)
			r.Post("/roles", h.CreateRole)
			r.Patch("/roles/{roleID}", h.UpdateRole)
			r.Delete("/roles/{roleID}", h.DeleteRole)
			r.Patch("/members/{membershipID}/role", h.AssignMemberRole)
			// Nested tenant-scoped routes use the middleware so handlers can rely
			// on authctx.TenantFromContext.
			r.Group(func(r chi.Router) {
				r.Use(tenantMW.RequireTenant)
				r.Get("/members", h.ListMembers)
			})
		})
	})
}
