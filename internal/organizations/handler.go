package organizations

import (
	"net/http"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/validation"
)

// Handler exposes organization HTTP endpoints.
type Handler struct {
	service *Service
	logger  *slog.Logger
}

// NewHandler constructs an organizations Handler.
func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func currentUserID(r *http.Request) (uuid.UUID, bool) {
	p, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		return uuid.Nil, false
	}
	return p.UserID, true
}

// ListMine handles GET /organizations (orgs the caller belongs to — the
// organization-switching list endpoint).
func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(r)
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	orgs, err := h.service.ListMyOrganizations(r.Context(), userID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, orgs)
}

// Create handles POST /organizations.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(r)
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	v := validation.New()
	v.Required("name", req.Name)
	v.MaxLen("name", req.Name, 150)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	org, err := h.service.CreateOrganization(r.Context(), userID, req.Name)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, org)
}

// Get handles GET /organizations/{orgID}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(r)
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return
	}
	org, err := h.service.GetOrganization(r.Context(), userID, orgID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, org)
}

// Update handles PATCH /organizations/{orgID}.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(r)
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	v := validation.New()
	v.Required("name", req.Name)
	v.MaxLen("name", req.Name, 150)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	org, err := h.service.UpdateOrganization(r.Context(), userID, orgID, req.Name)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, org)
}

// ListMembers handles GET /organizations/{orgID}/members.
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userID, ok := currentUserID(r)
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return
	}
	members, err := h.service.ListMembers(r.Context(), userID, orgID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, members)
}
