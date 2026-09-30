package organizations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/api"
	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

// newAppTestServer wires the real router against the test database, exactly as
// cmd/api does, so the public invitation flow is exercised end to end.
func newAppTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool, *recordingSender, string) {
	t.Helper()
	pool := testPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("http-test-secret", "teamflow", 15*time.Minute)
	orgSvc := organizations.NewService(pool, logger)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, logger)
	sender := &recordingSender{}

	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20
	cfg.Invite.BaseURL = "http://app.example.com"
	cfg.Invite.TTL = 7 * 24 * time.Hour
	cfg.APIKey.DefaultTTL = 90 * 24 * time.Hour
	cfg.APIKey.MaxTTL = 365 * 24 * time.Hour

	apiKeySvc := apikeys.NewService(pool, orgSvc, cfg.APIKey.DefaultTTL, cfg.APIKey.MaxTTL, logger)
	authMW := auth.NewMiddleware(jwt, logger).WithAPIKeys(apiKeySvc)

	router := api.NewRouter(api.Dependencies{
		Config:       cfg,
		Logger:       logger,
		Health:       health.NewHandler(logger, map[string]health.Checker{}),
		AuthHandler:  auth.NewHandler(authSvc, logger),
		AuthMW:       authMW,
		OrgHandler:   organizations.NewHandler(orgSvc, logger),
		OrgMW:        organizations.NewMiddleware(orgSvc, logger),
		TeamsHandler: teams.NewHandler(teams.NewService(pool, orgSvc, logger), logger),
		Projects:     projects.NewHandler(projects.NewService(pool, orgSvc), logger),
		Tasks:        tasks.NewHandler(tasks.NewService(pool, orgSvc), logger),
		Invitations: invitations.NewHandler(
			invitations.NewService(pool, orgSvc, authSvc, sender, nil, cfg.Invite.TTL, cfg.Invite.BaseURL, logger),
			logger),
		APIKeys: apikeys.NewHandler(apiKeySvc, logger),
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, pool, sender, "http-test-secret"
}

func doJSON(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return resp.StatusCode, decoded
}

// rawJSON issues a request without asserting the response shape, which is how
// the token-not-in-response guarantee is checked.
// doJSONWithKey issues a request authenticated with an API key instead of a
// bearer token.
func doJSONWithKey(t *testing.T, srv *httptest.Server, method, path, apiKey, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(apiKeyHeader, apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return resp.StatusCode, decoded
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func rawJSON(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

func TestInvitationRoutesOverHTTP(t *testing.T) {
	srv, pool, sender, _ := newAppTestServer(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("http-test-secret", "teamflow", 15*time.Minute)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, logger)

	registered, err := authSvc.Register(ctx, auth.RegisterInput{
		Email: "http-invite@example.com", Password: "StrongPassword123",
		FirstName: "Http", LastName: "Inviter", OrganizationName: "HTTP Invitations Org",
	}, auth.RequestMeta{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token := registered.Tokens.AccessToken
	orgSvc := organizations.NewService(pool, logger)
	mine, err := orgSvc.ListMyOrganizations(ctx, registered.User.ID)
	if err != nil || len(mine) == 0 {
		t.Fatalf("list organizations: %v", err)
	}
	orgID := mine[0].ID
	base := "/api/v1/organizations/" + orgID.String()

	// Management endpoints require authentication.
	if status, _ := rawJSON(t, srv, http.MethodGet, base+"/invitations", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("GET invitations without a token = %d, want 401", status)
	}

	roleID := roleIDOf(t, pool, orgID, "Member")
	createBody := `{"email":"newcomer@example.com","role_id":"` + roleID.String() + `"}`
	status, created := doJSON(t, srv, http.MethodPost, base+"/invitations", token, createBody)
	if status != http.StatusCreated {
		t.Fatalf("create invitation = %d, %s", status, created)
	}
	data, ok := created["data"].(map[string]any)
	if !ok || data["status"] != invitations.StatusPending || data["email"] != "newcomer@example.com" {
		t.Fatalf("unexpected invitation payload: %v", created)
	}
	// Capture this invitation's link before creating the next one.
	link := sender.lastLink(t)
	inviteToken := tokenFromLink(t, link)

	// The response must not carry the token or the accept link.
	status, raw := rawJSON(t, srv, http.MethodPost, base+"/invitations", token,
		`{"email":"second@example.com","role_id":"`+roleID.String()+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create second invitation = %d", status)
	}
	secondLink := sender.lastLink(t)
	if bytes.Contains([]byte(raw), []byte("token=")) || bytes.Contains([]byte(raw), []byte(secondLink)) {
		t.Fatalf("invitation response leaked a usable link: %s", raw)
	}

	// The public preview endpoint needs no token.
	status, preview := doJSON(t, srv, http.MethodGet,
		"/api/v1/invitations/"+url.PathEscape(inviteToken), "", "")
	if status != http.StatusOK {
		t.Fatalf("preview = %d, %s", status, preview)
	}
	previewData := preview["data"].(map[string]any)
	if previewData["organization_name"] != "HTTP Invitations Org" || previewData["role_name"] != "Member" {
		t.Fatalf("unexpected preview: %v", previewData)
	}
	status, missing := doJSON(t, srv, http.MethodGet, "/api/v1/invitations/definitely-not-a-token", "", "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown token = %d, %s", status, missing)
	}
	errBody := missing["error"].(map[string]any)
	if errBody["code"] != "INVITATION_NOT_FOUND" || errBody["request_id"] == nil {
		t.Fatalf("unexpected error envelope: %v", errBody)
	}

	// A new account can be created and signed in through one public call.
	status, accepted := doJSON(t, srv, http.MethodPost, "/api/v1/invitations/accept", "",
		`{"token":"`+inviteToken+`","password":"StrongPassword123","first_name":"New","last_name":"Comer"}`)
	if status != http.StatusCreated {
		t.Fatalf("accept = %d, %s", status, accepted)
	}
	acceptData := accepted["data"].(map[string]any)
	accessToken, _ := acceptData["access_token"].(string)
	if accessToken == "" || acceptData["role"] != "Member" {
		t.Fatalf("unexpected accept payload: %v", acceptData)
	}
	org := acceptData["organization"].(map[string]any)
	if org["name"] != "HTTP Invitations Org" {
		t.Fatalf("unexpected organization in response: %v", org)
	}

	// The new session can immediately read the organization, which proves the
	// membership was created with the invited role.
	status, orgResponse := doJSON(t, srv, http.MethodGet, base, accessToken, "")
	if status != http.StatusOK {
		t.Fatalf("invitee read the organization = %d, %s", status, orgResponse)
	}
	if orgResponse["data"].(map[string]any)["role"] != "Member" {
		t.Fatalf("unexpected role: %v", orgResponse)
	}

	// The link is single use.
	status, replayed := doJSON(t, srv, http.MethodPost, "/api/v1/invitations/accept", "",
		`{"token":"`+inviteToken+`","password":"StrongPassword123","first_name":"New","last_name":"Comer"}`)
	if status != http.StatusConflict ||
		replayed["error"].(map[string]any)["code"] != "INVITATION_ALREADY_ACCEPTED" {
		t.Fatalf("replayed accept = %d, %s", status, replayed)
	}

	// Revoking invalidates the remaining link.
	secondToken := tokenFromLink(t, secondLink)
	status, revoked := doJSON(t, srv, http.MethodPost, base+"/invitations/"+lastInvitationID(t, sender)+"/revoke", token, "")
	if status != http.StatusOK {
		t.Fatalf("revoke = %d, %s", status, revoked)
	}
	status, revokedAccept := doJSON(t, srv, http.MethodPost, "/api/v1/invitations/accept", "",
		`{"token":"`+secondToken+`","password":"StrongPassword123","first_name":"A","last_name":"B"}`)
	if status != http.StatusGone {
		t.Fatalf("accept after revoke = %d, %s", status, revokedAccept)
	}
	if code := revokedAccept["error"].(map[string]any)["code"]; code != "INVITATION_REVOKED" {
		t.Fatalf("unexpected code after revoke: %v", code)
	}

	// Listing is paginated and filterable.
	status, list := doJSON(t, srv, http.MethodGet, base+"/invitations?status=revoked&page_size=10", token, "")
	if status != http.StatusOK {
		t.Fatalf("list = %d, %s", status, list)
	}
	if list["pagination"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("unexpected pagination: %v", list["pagination"])
	}
}

// lastInvitationID returns the id recorded in the most recent delivered
// message's metadata.
func lastInvitationID(t *testing.T, sender *recordingSender) string {
	t.Helper()
	if len(sender.messages) == 0 {
		t.Fatal("no invitation email was delivered")
	}
	return sender.messages[len(sender.messages)-1].Metadata["invitation_id"]
}
