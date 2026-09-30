package invitations

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
)

func TestCreateInputValidation(t *testing.T) {
	roleID := uuid.New()
	if err := (CreateInput{Email: "invitee@example.com", RoleID: roleID}).validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, in := range []CreateInput{
		{Email: "  ", RoleID: roleID},
		{Email: "not-an-email", RoleID: roleID},
		{Email: "invitee@example.com"},
	} {
		if err := in.validate(); err == nil {
			t.Fatalf("expected validation failure for %+v", in)
		}
	}
}

func TestAcceptInputValidation(t *testing.T) {
	// An authenticated caller sends only a token.
	if err := (AcceptInput{Token: "abc"}).validateAccount(); err != nil {
		t.Fatalf("token-only accept rejected: %v", err)
	}
	// Account creation requires a strong password and a name.
	valid := AcceptInput{Token: "abc", Password: "StrongPassword123", FirstName: "Ada", LastName: "L"}
	if err := valid.validateAccount(); err != nil {
		t.Fatalf("valid accept rejected: %v", err)
	}
	for _, in := range []AcceptInput{
		{Token: "abc", Password: "short"},
		{Token: "abc", Password: "StrongPassword123", FirstName: "  "},
		{Token: "abc", Password: "StrongPassword123", LastName: "  "},
	} {
		if err := in.validateAccount(); err == nil {
			t.Fatalf("expected validation failure for %+v", in)
		}
	}
}

func TestTokensAreUniqueAndHashed(t *testing.T) {
	first, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("tokens must not repeat")
	}
	if len(first) != 43 {
		t.Fatalf("token length = %d, want 43 base64url characters", len(first))
	}
	if strings.ContainsAny(first, "+/=") {
		t.Fatalf("token is not URL safe: %q", first)
	}
	// The digest is stable, so a resent link can be verified, and the raw token
	// is never recoverable from it.
	if hashToken(first) != hashToken(first) || hashToken(first) == hashToken(second) {
		t.Fatal("hashing is not a stable one-way function")
	}
	if len(hashToken(first)) != 64 {
		t.Fatalf("hash length = %d, want 64 hex characters", len(hashToken(first)))
	}
}

func TestAcceptURLUsesQueryParameter(t *testing.T) {
	token, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	link := acceptURL("http://localhost:3000/", token)
	if !strings.HasPrefix(link, "http://localhost:3000/invitations/accept?token=") {
		t.Fatalf("unexpected link %q", link)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("token") != token {
		t.Fatalf("token round trip failed: %s", link)
	}
}

func TestEffectiveStatusReportsExpiredPendingInvitations(t *testing.T) {
	now := time.Now()
	pending := db.Invitation{Status: StatusPending, ExpiresAt: now.Add(time.Hour)}
	if got := effectiveStatus(pending, now); got != StatusPending {
		t.Fatalf("pending invitation status = %q", got)
	}
	stale := db.Invitation{Status: StatusPending, ExpiresAt: now.Add(-time.Minute)}
	if got := effectiveStatus(stale, now); got != StatusExpired {
		t.Fatalf("lapsed invitation status = %q, want expired", got)
	}
	if isUsable(stale, now) {
		t.Fatal("a lapsed invitation must not be usable")
	}
	if !isUsable(pending, now) {
		t.Fatal("a live pending invitation must be usable")
	}
	accepted := db.Invitation{Status: StatusAccepted, ExpiresAt: now.Add(time.Hour)}
	if isUsable(accepted, now) {
		t.Fatal("an accepted invitation must not be usable")
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got := normalizeEmail("  Invitee@Example.COM "); got != "invitee@example.com" {
		t.Fatalf("normalizeEmail = %q", got)
	}
}

func TestInvitationDTOOmitsToken(t *testing.T) {
	dto := toDTO(db.Invitation{
		ID: uuid.New(), OrganizationID: uuid.New(), Email: "invitee@example.com",
		RoleID: uuid.New(), Status: StatusPending, TokenHash: "deadbeef",
	}, "Member", time.Now())

	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "deadbeef") || strings.Contains(string(raw), "token") {
		t.Fatalf("DTO leaked the stored token hash: %s", raw)
	}
	if !strings.Contains(string(raw), `"role_name":"Member"`) {
		t.Fatalf("unexpected DTO: %s", raw)
	}
}

func TestParseListFilter(t *testing.T) {
	filter, err := parseListFilter(url.Values{
		"status": {"pending"}, "email": {" Invitee@Example.com "},
		"sort": {"expires_at"}, "order": {"asc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filter.Status == nil || *filter.Status != StatusPending {
		t.Fatalf("unexpected status filter: %+v", filter)
	}
	if filter.Email == nil || *filter.Email != "invitee@example.com" {
		t.Fatalf("email filter must be normalized: %+v", filter)
	}
	if filter.Sort != "expires_at" || filter.Desc {
		t.Fatalf("unexpected sort: %+v", filter)
	}

	defaults, err := parseListFilter(url.Values{})
	if err != nil || defaults.Sort != defaultSort || !defaults.Desc || defaults.Status != nil {
		t.Fatalf("unexpected defaults: %+v, %v", defaults, err)
	}
}

func TestParseListFilterRejectsUnsafeInput(t *testing.T) {
	cases := []url.Values{
		{"status": {"pending' OR 1=1 --"}},
		{"email": {"not-an-email"}},
		{"sort": {"token_hash"}},
		{"sort": {"email; DROP TABLE invitations"}},
		{"order": {"sideways"}},
	}
	for _, values := range cases {
		if _, err := parseListFilter(values); err == nil {
			t.Fatalf("expected %v to be rejected", values)
		}
	}
}
