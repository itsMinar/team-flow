package auth

import "github.com/go-chi/chi/v5"

// RegisterRoutes mounts the authentication endpoints on r. Public endpoints
// (register, login, refresh, logout) require no access token; logout-all and me
// are protected by the authentication middleware.
func (h *Handler) RegisterRoutes(r chi.Router, mw *Middleware) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.Post("/refresh", h.Refresh)
		r.Post("/logout", h.Logout)

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAuth)
			r.Post("/logout-all", h.LogoutAll)
			r.Get("/me", h.Me)
		})
	})
}
