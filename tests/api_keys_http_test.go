package organizations_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/organizations"
)

const apiKeyHeader = "X-API-Key"

// keyRequest issues a request with an API key instead of a bearer token.
func keyRequest(t *testing.T, srvURL, method, path, apiKey string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srvURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(apiKeyHeader, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(payload)
}

func TestAPIKeyAuthenticationOverHTTP(t *testing.T) {
	srv, pool, _, secret := newAppTestServer(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	authSvc := auth.NewService(pool, auth.NewJWTService(secret, "teamflow", 15*time.Minute), 720*time.Hour, logger)

	// An owner registers, then mints a key through the HTTP API.
	registered, err := authSvc.Register(ctx, auth.RegisterInput{
		Email: "http-key@example.com", Password: "StrongPassword123",
		FirstName: "Http", LastName: "Key", OrganizationName: "HTTP API Keys Org",
	}, auth.RequestMeta{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token := registered.Tokens.AccessToken
	mine, err := orgSvc.ListMyOrganizations(ctx, registered.User.ID)
	if err != nil || len(mine) == 0 {
		t.Fatalf("list organizations: %v", err)
	}
	orgID := mine[0].ID
	base := "/api/v1/organizations/" + orgID.String()

	// No credential at all is rejected.
	if status, _ := doJSON(t, srv, http.MethodPost, base+"/projects", "", `{"name":"Nope"}`); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated project create = %d, want 401", status)
	}

	status, created := doJSON(t, srv, http.MethodPost, base+"/api-keys", token, `{"name":"CI pipeline"}`)
	if status != http.StatusCreated {
		t.Fatalf("create api key = %d, %s", status, created)
	}
	data := created["data"].(map[string]any)
	secretKey, _ := data["key"].(string)
	keyMeta := data["api_key"].(map[string]any)
	if !strings.HasPrefix(secretKey, "tfk_") || keyMeta["status"] != apikeys.StatusActive {
		t.Fatalf("unexpected creation response: %s", created)
	}
	if data["warning"] == nil {
		t.Fatal("the one-time warning must accompany the secret")
	}
	// The public identifiers are safe to display.
	if keyMeta["key_prefix"] != secretKey[:12] || keyMeta["key_last_four"] != secretKey[len(secretKey)-4:] {
		t.Fatalf("identifiers do not match the key: %s", created)
	}

	// The key can read and write the organization's resources.
	projectPath := base + "/projects"
	status, body := doJSONWithKey(t, srv, http.MethodPost, projectPath, secretKey, `{"name":"Created by key"}`)
	if status != http.StatusCreated {
		t.Fatalf("key project create = %d, %s", status, body)
	}
	status, body = doJSONWithKey(t, srv, http.MethodGet, projectPath, secretKey, "")
	if status != http.StatusOK {
		t.Fatalf("key project list = %d, %s", status, body)
	}
	if !strings.Contains(mustMarshal(t, body), "Created by key") {
		t.Fatalf("key cannot read its organization's projects: %s", body)
	}

	// The key is pinned to its organization and rejected elsewhere, including on
	// session-only endpoints.
	otherUser, _ := registerOrg(t, pool, "http-other@example.com", "HTTP Other Org")
	otherMine, err := orgSvc.ListMyOrganizations(ctx, otherUser)
	if err != nil || len(otherMine) == 0 {
		t.Fatalf("list organizations: %v", err)
	}
	otherBase := "/api/v1/organizations/" + otherMine[0].ID.String()
	if status, _ = keyRequest(t, srv.URL, http.MethodGet, otherBase+"/projects", secretKey); status != http.StatusUnauthorized {
		t.Fatalf("cross-organization key = %d, want 401", status)
	}
	if status, _ = keyRequest(t, srv.URL, http.MethodGet, "/api/v1/auth/me", secretKey); status != http.StatusUnauthorized {
		t.Fatalf("key on /auth/me = %d, want 401", status)
	}
	if status, _ = keyRequest(t, srv.URL, http.MethodGet, "/api/v1/organizations", secretKey); status != http.StatusUnauthorized {
		t.Fatalf("key on the organization listing = %d, want 401", status)
	}

	// Revoking the key takes effect on the next request.
	keyID := keyMeta["id"].(string)
	status, revoked := doJSON(t, srv, http.MethodDelete, base+"/api-keys/"+keyID, token, "")
	if status != http.StatusOK {
		t.Fatalf("revoke = %d, %s", status, revoked)
	}
	if status, _ = keyRequest(t, srv.URL, http.MethodGet, projectPath, secretKey); status != http.StatusUnauthorized {
		t.Fatalf("revoked key = %d, want 401", status)
	}
	// The bearer session still works.
	if status, _ = doJSON(t, srv, http.MethodGet, projectPath, token, ""); status != http.StatusOK {
		t.Fatalf("bearer session after revoke = %d, want 200", status)
	}

	// A junk key is rejected as a plain authentication failure: the response does
	// not distinguish an unknown key from a revoked or expired one.
	status, junk := keyRequest(t, srv.URL, http.MethodGet, projectPath, "tfk_"+strings.Repeat("z", 43))
	if status != http.StatusUnauthorized || !strings.Contains(junk, "UNAUTHORIZED") {
		t.Fatalf("junk key = %d, %s", status, junk)
	}

	// Listing keys never returns the secret.
	status, listed := doJSON(t, srv, http.MethodGet, base+"/api-keys?include_revoked=true", token, "")
	if status != http.StatusOK {
		t.Fatalf("list keys = %d, %s", status, listed)
	}
	if strings.Contains(mustMarshal(t, listed), secretKey) {
		t.Fatalf("listing leaked the key secret: %s", listed)
	}
}
