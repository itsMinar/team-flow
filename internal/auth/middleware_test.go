package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
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

func TestOptionalAuth(t *testing.T) {
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
	middleware := NewMiddleware(service, slog.New(slog.NewTextHandler(io.Discard, nil)))

	tests := []struct {
		name       string
		header     string
		wantAuthed bool
	}{
		{name: "missing", wantAuthed: false},
		{name: "malformed", header: "Bearer invalid"},
		{name: "expired", header: "Bearer " + expired},
		{name: "valid", header: "Bearer " + valid, wantAuthed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := middleware.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				principal, ok := authctx.PrincipalFromContext(request.Context())
				// An unauthenticated request must always reach the handler.
				if ok != test.wantAuthed {
					t.Fatalf("authenticated = %v, want %v", ok, test.wantAuthed)
				}
				if ok && (principal.UserID != userID || principal.TokenID != tokenID) {
					t.Fatalf("unexpected principal: %+v", principal)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/invitations/accept", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", recorder.Code)
			}
		})
	}
}

// stubAPIKeys is a minimal APIKeyAuthenticator for middleware tests.
type stubAPIKeys struct {
	principal authctx.Principal
	err       error
	calls     int
	keys      []string
}

func (s *stubAPIKeys) Authenticate(_ context.Context, raw string) (authctx.Principal, error) {
	s.calls++
	s.keys = append(s.keys, raw)
	return s.principal, s.err
}

func newAPIKeyMiddleware(stub *stubAPIKeys) (*Middleware, uuid.UUID) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewJWTService("test-secret", "teamflow", time.Minute)
	orgID := uuid.New()
	stub.principal = authctx.Principal{
		UserID:         uuid.New(),
		Method:         authctx.MethodAPIKey,
		APIKeyID:       uuid.New(),
		OrganizationID: orgID,
	}
	return NewMiddleware(service, logger).WithAPIKeys(stub), orgID
}

func TestRequireAuthAcceptsAPIKeysOnScopedRoutes(t *testing.T) {
	stub := &stubAPIKeys{}
	middleware, orgID := newAPIKeyMiddleware(stub)
	apiKey := "tfk_" + strings.Repeat("a", 43)

	router := chi.NewRouter()
	router.Route("/organizations/{orgID}/projects", func(r chi.Router) {
		r.Use(middleware.RequireAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			principal, ok := authctx.PrincipalFromContext(r.Context())
			if !ok || !principal.IsAPIKey() || principal.OrganizationID != orgID {
				t.Errorf("unexpected principal: %+v", principal)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/organizations/"+orgID.String()+"/projects", nil)
	request.Header.Set(apiKeyHeader, apiKey)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", recorder.Code, recorder.Body.String())
	}
	if stub.calls != 1 || stub.keys[0] != apiKey {
		t.Fatalf("authenticator was not called with the presented key: %+v", stub)
	}
}

// An API key is pinned to one organization and may only be presented there.
func TestRequireAuthRejectsAPIKeyOutsideItsOrganization(t *testing.T) {
	stub := &stubAPIKeys{}
	middleware, orgID := newAPIKeyMiddleware(stub)
	otherOrg := uuid.New()

	var principal authctx.Principal
	handler := middleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ = authctx.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chi.RouteContext(r.Context()).URLParams.Add("orgID", otherOrg.String())
			next.ServeHTTP(w, r)
		})
	})
	router.Get("/organizations/{orgID}/projects", handler.ServeHTTP)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/organizations/"+otherOrg.String()+"/projects", nil)
	request.Header.Set(apiKeyHeader, "tfk_"+strings.Repeat("a", 43))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("cross-organization key = %d, want 401", recorder.Code)
	}
	if principal != (authctx.Principal{}) {
		t.Fatalf("no principal may be stored for a rejected key: %+v", principal)
	}
	_ = orgID
}

func TestRequireAuthRejectsInvalidAPIKey(t *testing.T) {
	stub := &stubAPIKeys{err: errors.New("invalid or expired API key")}
	middleware, orgID := newAPIKeyMiddleware(stub)

	router := chi.NewRouter()
	router.Route("/organizations/{orgID}/projects", func(r chi.Router) {
		r.Use(middleware.RequireAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			t.Error("handler must not run for a rejected API key")
		})
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/organizations/"+orgID.String()+"/projects", nil)
	request.Header.Set(apiKeyHeader, "tfk_"+strings.Repeat("b", 43))
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// Without an API key authenticator configured, the header must be ignored.
func TestRequireAuthIgnoresAPIKeyHeaderWhenNotConfigured(t *testing.T) {
	middleware := NewMiddleware(NewJWTService("test-secret", "teamflow", time.Minute),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := middleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler must not run without a valid credential")
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/organizations/some-org/projects", nil)
	request.Header.Set(apiKeyHeader, "tfk_"+strings.Repeat("c", 43))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestOptionalAuthIgnoresInvalidAPIKey(t *testing.T) {
	stub := &stubAPIKeys{err: errors.New("invalid")}
	middleware, _ := newAPIKeyMiddleware(stub)
	reached := false
	handler := middleware.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authctx.PrincipalFromContext(r.Context()); ok {
			t.Error("an invalid key must not produce a principal")
		}
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept", nil)
	request.Header.Set(apiKeyHeader, "tfk_"+strings.Repeat("d", 43))
	handler.ServeHTTP(recorder, request)
	if !reached || recorder.Code != http.StatusNoContent {
		t.Fatalf("the handler must still run: reached=%v status=%d", reached, recorder.Code)
	}
}
