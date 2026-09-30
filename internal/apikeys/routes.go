package apikeys

import (
	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/auth"
)

// RegisterRoutes mounts the organization-scoped API key endpoints. Managing keys
// mints credentials, so it requires api_keys.manage and a bearer session: the
// endpoints accept either credential like the rest of the tenant routes.
func (h *Handler) RegisterRoutes(r chi.Router, authMW *auth.Middleware) {
	r.Route("/organizations/{orgID}/api-keys", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Delete("/{apiKeyID}", h.Revoke)
	})
}
