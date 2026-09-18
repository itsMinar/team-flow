package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/authctx"
)

func TestRequireAuth(t *testing.T) {
	service := NewJWTService("test-secret", "teamflow", time.Minute)
	userID, tokenID := uuid.New(), uuid.New()
	valid, _, err := service.GenerateAccessToken(userID, tokenID)
	if err != nil {
		t.Fatal(err)
	}
	expired, _, err := NewJWTService("test-secret", "teamflow", -time.Minute).GenerateAccessToken(userID, tokenID)
	if err != nil {
		t.Fatal(err)
	}
	noExpiry, err := jwt.NewWithClaims(jwt.SigningMethodHS256, AccessClaims{
		Type: accessTokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID.String(),
			ID:      tokenID.String(),
			Issuer:  "teamflow",
		},
	}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	middleware := NewMiddleware(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tests := []struct {
		name   string
		header string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "empty bearer", header: "Bearer ", status: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic " + valid, status: http.StatusUnauthorized},
		{name: "malformed", header: "Bearer invalid", status: http.StatusUnauthorized},
		{name: "expired", header: "Bearer " + expired, status: http.StatusUnauthorized},
		{name: "missing expiration", header: "Bearer " + noExpiry, status: http.StatusUnauthorized},
		{name: "valid", header: "Bearer " + valid, status: http.StatusNoContent},
		{name: "case insensitive scheme", header: "bEaReR " + valid, status: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := middleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				called = true
				principal, ok := authctx.PrincipalFromContext(request.Context())
				if !ok || principal.UserID != userID || principal.TokenID != tokenID {
					t.Error("authenticated principal is missing or incorrect")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			request.Header.Set("Authorization", test.header)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if called != (test.status == http.StatusNoContent) {
				t.Fatalf("handler called = %v for status %d", called, test.status)
			}
		})
	}
}
