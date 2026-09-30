package auth

import (
	"context"
	"net/http"
	"strings"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// apiKeyHeader carries an organization API key. It is a separate header from
// Authorization so the two credential types can never be confused, and so a
// proxy or log that captures Authorization still sees no long-lived secret.
const apiKeyHeader = "X-API-Key"

// APIKeyAuthenticator resolves a raw API key to the principal it authenticates.
// The auth package depends on this interface rather than on the API key package,
// so credential resolution stays free of import cycles.
type APIKeyAuthenticator interface {
	// Authenticate returns the principal for a valid, unexpired, unrevoked key.
	// Any failure must return an error and no principal.
	Authenticate(ctx context.Context, rawKey string) (authctx.Principal, error)
}

// Middleware provides authentication as HTTP middleware.
type Middleware struct {
	jwt    *JWTService
	apiKey APIKeyAuthenticator
	logger *slog.Logger
}

// NewMiddleware constructs an authentication Middleware.
func NewMiddleware(jwt *JWTService, logger *slog.Logger) *Middleware {
	return &Middleware{jwt: jwt, logger: logger}
}

// WithAPIKeys enables API key authentication in addition to bearer tokens.
// Requests may then present either credential.
func (m *Middleware) WithAPIKeys(authenticator APIKeyAuthenticator) *Middleware {
	m.apiKey = authenticator
	return m
}

// RequireAuth validates the presented credential and stores the resulting
// principal in the request context. A bearer access token is accepted first,
// then an API key. Requests without a usable credential are rejected with 401
// before reaching the handler.
//
// The middleware is idempotent: when a principal is already in the context the
// request passes straight through. That lets the router authenticate once at the
// top of a route group, mount per-caller concerns such as rate limiting after it,
// and still let individual feature routers apply RequireAuth themselves.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, already := authctx.PrincipalFromContext(r.Context()); already {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := m.resolve(r)
		if !ok {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), principal)))
	})
}

// OptionalAuth stores the principal when a valid credential is present and
// otherwise lets the request through unauthenticated.
//
// It is used by public endpoints that behave differently for signed-in users,
// such as accepting an invitation. An invalid or missing credential is never an
// error here, so this must not be used to protect anything.
func (m *Middleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := m.resolve(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(authctx.WithPrincipal(r.Context(), principal)))
	})
}

// resolve authenticates the request from a bearer token or an API key.
func (m *Middleware) resolve(r *http.Request) (authctx.Principal, bool) {
	if token, ok := bearerToken(r); ok {
		return m.resolveBearer(token)
	}
	return m.resolveAPIKey(r)
}

func (m *Middleware) resolveBearer(token string) (authctx.Principal, bool) {
	claims, err := m.jwt.ParseAccessToken(token)
	if err != nil {
		return authctx.Principal{}, false
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return authctx.Principal{}, false
	}
	tokenID, _ := uuid.Parse(claims.ID)
	return authctx.Principal{
		UserID:  userID,
		TokenID: tokenID,
		Method:  authctx.MethodBearer,
	}, true
}

func (m *Middleware) resolveAPIKey(r *http.Request) (authctx.Principal, bool) {
	if m.apiKey == nil {
		return authctx.Principal{}, false
	}
	rawKey := strings.TrimSpace(r.Header.Get(apiKeyHeader))
	if rawKey == "" {
		return authctx.Principal{}, false
	}
	principal, err := m.apiKey.Authenticate(r.Context(), rawKey)
	if err != nil {
		// The key itself is never logged; the rejection is enough to investigate.
		m.logger.Warn("api key authentication failed", slog.Any("error", err))
		return authctx.Principal{}, false
	}
	// An API key is pinned to one organization, so it may only be presented on
	// organization-scoped routes. Anything else (including /auth/me and the
	// organization listing) is rejected rather than silently widening scope.
	orgID := chi.URLParam(r, "orgID")
	if orgID == "" || orgID != principal.OrganizationID.String() {
		m.logger.Warn("api key used outside its organization scope",
			slog.String("organization_id", principal.OrganizationID.String()),
			slog.String("path", r.URL.Path),
		)
		return authctx.Principal{}, false
	}
	return principal, true
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header. The header value itself is never logged.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	return token, token != ""
}
