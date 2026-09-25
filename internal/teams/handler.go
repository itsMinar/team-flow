package teams

import (
	"net/http"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/validation"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

func (h *Handler) userID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	principal, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return uuid.Nil, false
	}
	return principal.UserID, true
}

func (h *Handler) parseID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}

type teamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func validateTeamRequest(req teamRequest) error {
	v := validation.New()
	v.Required("name", req.Name)
	v.MaxLen("name", req.Name, maxTeamNameLength)
	v.MaxLen("description", req.Description, maxTeamDescriptionLength)
	return v.Err()
}

func descriptionPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teams, err := h.service.List(r.Context(), userID, orgID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, teams)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	team, err := h.service.Get(r.Context(), userID, orgID, teamID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, team)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	var req teamRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	if err := validateTeamRequest(req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	team, err := h.service.Create(r.Context(), userID, orgID, req.Name, descriptionPointer(req.Description))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, team)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	var req teamRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	if err := validateTeamRequest(req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	team, err := h.service.Update(r.Context(), userID, orgID, teamID, req.Name, descriptionPointer(req.Description))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, team)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), userID, orgID, teamID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	members, err := h.service.ListMembers(r.Context(), userID, orgID, teamID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, members)
}

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	var req struct {
		UserID uuid.UUID `json:"user_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	if req.UserID == uuid.Nil {
		httpx.WriteError(w, r, h.logger, httpx.NewValidationError(map[string]string{"user_id": "is required"}))
		return
	}
	member, err := h.service.AddMember(r.Context(), userID, orgID, teamID, req.UserID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, member)
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.userID(w, r)
	if !ok {
		return
	}
	orgID, ok := h.parseID(w, r, "orgID")
	if !ok {
		return
	}
	teamID, ok := h.parseID(w, r, "teamID")
	if !ok {
		return
	}
	memberID, ok := h.parseID(w, r, "teamMemberID")
	if !ok {
		return
	}
	if err := h.service.RemoveMember(r.Context(), userID, orgID, teamID, memberID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]bool{"deleted": true})
}
