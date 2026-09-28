package projects

import (
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

// parseListFilter accepts only whitelisted filter values and sort keys.
func parseListFilter(values url.Values) (ListFilter, error) {
	filter := ListFilter{Sort: defaultSort, Desc: true}
	errs := map[string]string{}
	if s := values.Get("status"); s != "" {
		if validStatuses[s] {
			filter.Status = &s
		} else {
			errs["status"] = "must be one of planning, active, on_hold, completed, archived"
		}
	}
	if p := values.Get("priority"); p != "" {
		if validPriorities[p] {
			filter.Priority = &p
		} else {
			errs["priority"] = "must be one of low, medium, high, urgent"
		}
	}
	if raw := values.Get("team_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			filter.TeamID = &id
		} else {
			errs["team_id"] = "must be a valid UUID"
		}
	}
	if s := values.Get("sort"); s != "" {
		if sortableFields[s] {
			filter.Sort = s
		} else {
			errs["sort"] = "must be one of created_at, updated_at, name, due_date, priority"
		}
	}
	switch values.Get("order") {
	case "", "desc":
	case "asc":
		filter.Desc = false
	default:
		errs["order"] = "must be asc or desc"
	}
	if len(errs) > 0 {
		return ListFilter{}, httpx.NewValidationError(errs)
	}
	return filter, nil
}

func (h *Handler) caller(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	principal, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok := h.pathID(w, r, "orgID")
	return principal.UserID, orgID, ok
}

func (h *Handler) pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	page, err := httpx.ParsePageRequest(r.URL.Query())
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	filter, err := parseListFilter(r.URL.Query())
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	projects, pagination, err := h.service.List(r.Context(), userID, orgID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, projects, pagination)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	project, err := h.service.Create(r.Context(), userID, orgID, in)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, project)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	project, err := h.service.Get(r.Context(), userID, orgID, projectID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, project)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	var in UpdateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	project, err := h.service.Update(r.Context(), userID, orgID, projectID, in)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, project)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), userID, orgID, projectID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *Handler) ListActivity(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	page, err := httpx.ParsePageRequest(r.URL.Query())
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	entries, pagination, err := h.service.ListActivity(r.Context(), userID, orgID, projectID, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, entries, pagination)
}
