package teams

import (
	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/auth"
)

func (h *Handler) RegisterRoutes(r chi.Router, authMW *auth.Middleware) {
	r.Route("/organizations/{orgID}/teams", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Route("/{teamID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Delete("/", h.Delete)
			r.Get("/members", h.ListMembers)
			r.Post("/members", h.AddMember)
			r.Delete("/members/{teamMemberID}", h.RemoveMember)
		})
	})
}
