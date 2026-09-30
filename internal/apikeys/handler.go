package apikeys

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

// parseListFilter accepts only whitelisted sort keys. include_revoked is opt-in,
// so an operator sees the live keys by default.
func parseListFilter(values url.Values) (ListFilter, error) {
	filter := ListFilter{Sort: defaultSort, Desc: true}
	errs := map[string]string{}
	switch values.Get("include_revoked") {
	case "", "false":
	case "true":
		filter.IncludeRevoked = true
	default:
		errs["include_revoked"] = "must be true or false"
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
	orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return uuid.Nil, uuid.Nil, false
	}
	return principal.UserID, orgID, true
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
	keys, pagination, err := h.service.List(r.Context(), userID, orgID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, keys, pagination)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var req struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expires_in_days"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	created, err := h.service.Create(r.Context(), userID, orgID, CreateInput{
		Name: req.Name, ExpiresInDays: req.ExpiresInDays,
	})
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusCreated, created)
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	keyID, err := uuid.Parse(chi.URLParam(r, "apiKeyID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
		return
	}
	if err := h.service.Revoke(r.Context(), userID, orgID, keyID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]bool{"revoked": true})
}
