package invitations

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/validation"
)

// maxTokenLength bounds the token accepted from a URL or body. Real tokens are
// 43 characters, so anything longer is a malformed request rather than a token.
const maxTokenLength = 512

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
			errs["status"] = statusList
		}
	}
	if raw := values.Get("email"); raw != "" {
		email := normalizeEmail(raw)
		v := validation.New()
		v.Email("email", email)
		if err := v.Err(); err != nil {
			errs["email"] = "must be a valid email address"
		} else {
			filter.Email = &email
		}
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

// tokenParam reads the invitation token from the URL. It is only ever hashed;
// it is never logged or echoed back to the client.
func (h *Handler) tokenParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	token := strings.TrimSpace(chi.URLParam(r, name))
	if token == "" || len(token) > maxTokenLength {
		httpx.WriteError(w, r, h.logger, notFound(nil))
		return "", false
	}
	return token, true
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
	invitations, pagination, err := h.service.List(r.Context(), userID, orgID, filter, page)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WritePage(w, invitations, pagination)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	var req struct {
		Email  string    `json:"email"`
		RoleID uuid.UUID `json:"role_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	if req.RoleID == uuid.Nil {
		httpx.WriteError(w, r, h.logger, httpx.NewValidationError(map[string]string{"role_id": "is required"}))
		return
	}
	invitation, err := h.service.Create(r.Context(), userID, orgID, CreateInput{
		Email: normalizeEmail(req.Email), RoleID: req.RoleID,
	})
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	// The response deliberately omits the token: the invite link is only ever
	// delivered by email.
	httpx.WriteData(w, http.StatusCreated, invitation)
}

func (h *Handler) Resend(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	invitationID, ok := h.pathID(w, r, "invitationID")
	if !ok {
		return
	}
	invitation, err := h.service.Resend(r.Context(), userID, orgID, invitationID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, invitation)
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	userID, orgID, ok := h.caller(w, r)
	if !ok {
		return
	}
	invitationID, ok := h.pathID(w, r, "invitationID")
	if !ok {
		return
	}
	if err := h.service.Revoke(r.Context(), userID, orgID, invitationID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]bool{"revoked": true})
}

// Preview handles GET /invitations/{token}. It is public: the token is the
// credential, and the response is limited to what the invitee already knows.
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	token, ok := h.tokenParam(w, r, "token")
	if !ok {
		return
	}
	preview, err := h.service.Preview(r.Context(), token)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, preview)
}

// acceptRequest accepts an invitation either for a brand new account or for the
// already authenticated caller, who sends only the token.
type acceptRequest struct {
	Token     string `json:"token"`
	Password  string `json:"password"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type acceptResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Organization struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"organization"`
	Role string       `json:"role"`
	User auth.UserDTO `json:"user"`
}

func (h *Handler) Accept(w http.ResponseWriter, r *http.Request) {
	var req acceptRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	// OptionalAuth lets an existing member accept with their current session; an
	// anonymous caller creates the account with the supplied credentials.
	var userID uuid.UUID
	if principal, ok := authctx.PrincipalFromContext(r.Context()); ok {
		userID = principal.UserID
	}

	result, err := h.service.Accept(r.Context(), userID, AcceptInput{
		Token: req.Token, Password: req.Password,
		FirstName: req.FirstName, LastName: req.LastName,
	}, auth.RequestMetaFromRequest(r))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	resp := acceptResponse{
		AccessToken:  result.Tokens.AccessToken,
		RefreshToken: result.Tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    result.Tokens.ExpiresIn,
		Role:         result.RoleName,
		User:         result.User,
	}
	resp.Organization.ID = result.OrganizationID
	resp.Organization.Name = result.OrganizationName
	httpx.WriteData(w, http.StatusCreated, resp)
}
