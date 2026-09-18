package auth

import (
	"net/http"
	"strings"

	"log/slog"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// Middleware provides authentication as HTTP middleware.
type Middleware struct {
	jwt    *JWTService
	logger *slog.Logger
}

// NewMiddleware constructs an authentication Middleware.
func NewMiddleware(jwt *JWTService, logger *slog.Logger) *Middleware {
	return &Middleware{jwt: jwt, logger: logger}
}

// RequireAuth validates the Bearer access token and stores the resulting
// principal in the request context. Requests without a valid token are rejected
// with 401 before reaching the handler.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized)
			return
		}

		claims, err := m.jwt.ParseAccessToken(token)
		if err != nil {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized)
			return
		}

		userID, err := uuid.Parse(claims.Subject)
		if err != nil {
			httpx.WriteError(w, r, m.logger, httpx.ErrUnauthorized)
			return
		}
		tokenID, _ := uuid.Parse(claims.ID)

		ctx := authctx.WithPrincipal(r.Context(), authctx.Principal{
			UserID:  userID,
			TokenID: tokenID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
