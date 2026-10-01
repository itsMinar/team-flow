package audit

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"

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

// maxAuditWindow bounds the "since" filter so a single query cannot scan the whole
// log.
const maxAuditWindow = 90 * 24 * time.Hour

func parseListFilter(values url.Values) (ListFilter, error) {
	var filter ListFilter
	errs := map[string]string{}
	if action := values.Get("action"); action != "" {
		if len(action) > 100 {
			errs["action"] = "is too long"
		} else {
			filter.Action = &action
		}
	}
	if outcome := values.Get("outcome"); outcome != "" {
		switch outcome {
		case OutcomeSuccess, OutcomeFailure, OutcomeDenied:
			filter.Outcome = &outcome
		default:
			errs["outcome"] = "must be one of success, failure, denied"
		}
	}
	if actor := values.Get("actor_user_id"); actor != "" {
		id, err := uuid.Parse(actor)
		if err != nil {
			errs["actor_user_id"] = "must be a valid UUID"
		} else {
			filter.ActorUserID = &id
		}
	}
	if raw := values.Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		switch {
		case err != nil:
			errs["since"] = "must be an RFC 3339 timestamp"
		case parsed.Before(time.Now().Add(-maxAuditWindow)):
			errs["since"] = "must not be more than 90 days ago"
		default:
			filter.Since = &parsed
		}
	}
	if len(errs) > 0 {
		return ListFilter{}, httpx.NewValidationError(errs)
	}
	return filter, nil
}

// List handles GET /organizations/{orgID}/audit-logs.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	principal, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	orgID, err := uuid.Parse(chi.URLParam(r, "orgID"))
	if err != nil {
		httpx.WriteError(w, r, h.logger, httpx.ErrNotFound)
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
	entries, pagination, err := h.service.List(r.Context(), principal.UserID, orgID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, entries, pagination)
}
