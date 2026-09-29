package tasks

import (
	"github.com/go-chi/chi/v5"

	"github.com/itsMinar/team-flow/internal/auth"
)

// RegisterRoutes mounts the organization-scoped and project-scoped task routes.
// Every task is organization-scoped, so a project in the path is only ever a
// filter over the caller's own tenant.
func (h *Handler) RegisterRoutes(r chi.Router, authMW *auth.Middleware) {
	r.Route("/organizations/{orgID}/tasks", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.List)
		r.Route("/{taskID}", func(r chi.Router) {
			r.Get("/", h.Get)
			r.Patch("/", h.Update)
			r.Delete("/", h.Delete)
			r.Get("/activity", h.ListActivity)
		})
	})

	r.Route("/organizations/{orgID}/projects/{projectID}/tasks", func(r chi.Router) {
		r.Use(authMW.RequireAuth)
		r.Get("/", h.ListByProject)
		r.Post("/", h.Create)
	})
}
