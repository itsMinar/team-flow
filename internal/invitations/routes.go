package invitations

import (
	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/auth"
)

// RegisterRoutes mounts the organization-scoped invitation endpoints. Managing
// invitations requires an access token with members.manage.
func (h *Handler) RegisterRoutes(r chi.Router, authMW *auth.Middleware) {
	r.Route("/organizations/{orgID}/invitations", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Post("/{invitationID}/resend", h.Resend)
		r.Post("/{invitationID}/revoke", h.Revoke)
	})
}

// RegisterPublicRoutes mounts the accept flow.
//
// It is public because the invitation token is itself the credential.
// OptionalAuth lets an existing member redeem it with their current session while
// someone without an account creates one, so this must be mounted outside the
// authenticated and per-user rate limited group.
func (h *Handler) RegisterPublicRoutes(r chi.Router, authMW *auth.Middleware) {
	r.Route("/invitations", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authMW.OptionalAuth)
			r.Post("/accept", h.Accept)
		})
		r.Get("/{token}", h.Preview)
	})
}
