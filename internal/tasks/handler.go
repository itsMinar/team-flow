package tasks

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
	if raw := values.Get("project_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			filter.ProjectID = &id
		} else {
			errs["project_id"] = "must be a valid UUID"
		}
	}
	if s := values.Get("status"); s != "" {
		if validStatuses[s] {
			filter.Status = &s
		} else {
			errs["status"] = statusList
		}
	}
	if p := values.Get("priority"); p != "" {
		if validPriorities[p] {
			filter.Priority = &p
		} else {
			errs["priority"] = priorityList
		}
	}
	if raw := values.Get("assignee_id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			filter.AssigneeID = &id
		} else {
			errs["assignee_id"] = "must be a valid UUID"
		}
	}
	if values.Get("unassigned") == "true" {
		filter.Unassigned = true
	} else if raw := values.Get("unassigned"); raw != "" && raw != "false" {
		errs["unassigned"] = "must be true or false"
	}
	// The two assignee filters are mutually exclusive so a request can never
	// ask for "assigned to X and to nobody".
	if filter.AssigneeID != nil && filter.Unassigned {
		errs["assignee_id"] = "cannot be combined with unassigned"
		delete(errs, "unassigned")
	}
	if s := values.Get("sort"); s != "" {
		if sortableFields[s] {
			filter.Sort = s
		} else {
			errs["sort"] = sortableList
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

// List returns the organization's tasks, optionally filtered by project.
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
	tasks, pagination, err := h.service.List(r.Context(), userID, orgID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, tasks, pagination)
}

// ListByProject returns the tasks of one project. The project is always
// scoped by the URL, so a project_id query parameter cannot widen the result.
func (h *Handler) ListByProject(w http.ResponseWriter, r *http.Request) {
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
	filter, err := parseListFilter(r.URL.Query())
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	tasks, pagination, err := h.service.ListByProject(r.Context(), userID, orgID, projectID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, tasks, pagination)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	projectID, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	task, err := h.service.Create(r.Context(), userID, orgID, projectID, in)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, task)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	taskID, ok := h.pathID(w, r, "taskID")
	if !ok {
		return
	}
	task, err := h.service.Get(r.Context(), userID, orgID, taskID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, task)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	taskID, ok := h.pathID(w, r, "taskID")
	if !ok {
		return
	}
	var in UpdateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	task, err := h.service.Update(r.Context(), userID, orgID, taskID, in)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, task)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	taskID, ok := h.pathID(w, r, "taskID")
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), userID, orgID, taskID); err != nil {
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
	taskID, ok := h.pathID(w, r, "taskID")
	if !ok {
		return
	}
	page, err := httpx.ParsePageRequest(r.URL.Query())
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	entries, pagination, err := h.service.ListActivity(r.Context(), userID, orgID, taskID, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, entries, pagination)
}
