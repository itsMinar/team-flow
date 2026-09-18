package auth

import (
	"net"
	"net/http"
	"strings"

	"log/slog"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/validation"
)

// Handler exposes the authentication HTTP endpoints.
type Handler struct {
	service *Service
	logger  *slog.Logger
}

// NewHandler constructs an auth Handler.
func NewHandler(service *Service, logger *slog.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

type registerRequest struct {
	Email            string `json:"email"`
	Password         string `json:"password"`
	FirstName        string `json:"first_name"`
	LastName         string `json:"last_name"`
	OrganizationName string `json:"organization_name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type authResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    int     `json:"expires_in"`
	User         UserDTO `json:"user"`
}

func toAuthResponse(r *AuthResult) authResponse {
	return authResponse{
		AccessToken:  r.Tokens.AccessToken,
		RefreshToken: r.Tokens.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    r.Tokens.ExpiresIn,
		User:         r.User,
	}
}

// Register handles POST /auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	v := validation.New()
	v.Required("email", req.Email)
	v.Email("email", req.Email)
	v.Password("password", req.Password)
	v.Required("first_name", req.FirstName)
	v.MaxLen("first_name", req.FirstName, 100)
	v.Required("last_name", req.LastName)
	v.MaxLen("last_name", req.LastName, 100)
	v.Required("organization_name", req.OrganizationName)
	v.MaxLen("organization_name", req.OrganizationName, 150)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	result, err := h.service.Register(r.Context(), RegisterInput{
		Email:            validation.NormalizeEmail(req.Email),
		Password:         req.Password,
		FirstName:        req.FirstName,
		LastName:         req.LastName,
		OrganizationName: req.OrganizationName,
	}, requestMeta(r))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	httpx.WriteData(w, http.StatusCreated, toAuthResponse(result))
}

// Login handles POST /auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	v := validation.New()
	v.Required("email", req.Email)
	v.Required("password", req.Password)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	result, err := h.service.Login(r.Context(), LoginInput{
		Email:    validation.NormalizeEmail(req.Email),
		Password: req.Password,
	}, requestMeta(r))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	httpx.WriteData(w, http.StatusOK, toAuthResponse(result))
}

// Refresh handles POST /auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	v := validation.New()
	v.Required("refresh_token", req.RefreshToken)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	result, err := h.service.Refresh(r.Context(), req.RefreshToken, requestMeta(r))
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	httpx.WriteData(w, http.StatusOK, toAuthResponse(result))
}

// Logout handles POST /auth/logout. It revokes the presented refresh token's
// session lineage.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	v := validation.New()
	v.Required("refresh_token", req.RefreshToken)
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}

	if err := h.service.Logout(r.Context(), req.RefreshToken); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// LogoutAll handles POST /auth/logout-all. It requires authentication and
// revokes every refresh token for the current user.
func (h *Handler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	principal, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	if err := h.service.LogoutAll(r.Context(), principal.UserID); err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, map[string]string{"status": "logged_out_all"})
}

// Me handles GET /auth/me. It returns the authenticated user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	principal, ok := authctx.PrincipalFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, h.logger, httpx.ErrUnauthorized)
		return
	}
	user, err := h.service.CurrentUser(r.Context(), principal.UserID)
	if err != nil {
		httpx.WriteError(w, r, h.logger, err)
		return
	}
	httpx.WriteData(w, http.StatusOK, user)
}

func requestMeta(r *http.Request) RequestMeta {
	return RequestMeta{
		UserAgent: r.UserAgent(),
		IPAddress: clientIP(r),
	}
}

// clientIP extracts a bare IP address (no port) suitable for storage in an
// inet column, preferring the first X-Forwarded-For entry when present.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return ""
}
