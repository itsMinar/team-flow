package apikeys

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
	if err := (CreateInput{Name: "CI pipeline"}).validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, in := range []CreateInput{
		{Name: "   "},
		{Name: strings.Repeat("k", maxNameLengthForTest+1)},
		{Name: "CI", ExpiresInDays: -1},
	} {
		if err := in.validate(); err == nil {
			t.Fatalf("expected validation failure for %+v", in)
		}
	}
}

// maxNameLengthForTest mirrors the database CHECK on api_keys.name so the test
// fails if the service and the schema drift apart.
const maxNameLengthForTest = 100

func TestGeneratedKeysAreUniquePrefixedAndHashed(t *testing.T) {
	first, prefix, lastFour, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("keys must not repeat")
	}
	if !strings.HasPrefix(first, keyPrefixMarker) {
		t.Fatalf("key is not recognizable: %q", first)
	}
	if len(first) != len(keyPrefixMarker)+43 {
		t.Fatalf("key length = %d, want %d characters", len(first), len(keyPrefixMarker)+43)
	}
	if prefix != first[:prefixLength] || lastFour != first[len(first)-lastFourChars:] {
		t.Fatalf("prefix/last four do not match the key: %q %q", prefix, lastFour)
	}
	if !validKeyFormat(first) || !validKeyFormat(second) {
		t.Fatal("generated keys must pass format validation")
	}
	// The digest is stable, and the raw key is not recoverable from it.
	if hashKey(first) != hashKey(first) || hashKey(first) == hashKey(second) {
		t.Fatal("hashing is not a stable one-way function")
	}
	if len(hashKey(first)) != 64 {
		t.Fatalf("hash length = %d, want 64 hex characters", len(hashKey(first)))
	}
	if strings.Contains(hashKey(first), keyPrefixMarker) {
		t.Fatal("the stored digest must not contain the key")
	}
}

func TestValidKeyFormatRejectsJunk(t *testing.T) {
	invalid := []string{
		"",
		"tfk_short",
		"sk-" + strings.Repeat("a", 43),
		keyPrefixMarker + strings.Repeat("!", 43),
		keyPrefixMarker + strings.Repeat("a", 42),
		keyPrefixMarker + strings.Repeat("a", 44),
	}
	for _, raw := range invalid {
		if validKeyFormat(raw) {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
}

func TestStatusDerivation(t *testing.T) {
	now := time.Now()
	active := db.ApiKey{ExpiresAt: now.Add(time.Hour)}
	if got := status(active, now); got != StatusActive {
		t.Fatalf("status = %q, want active", got)
	}
	expired := db.ApiKey{ExpiresAt: now.Add(-time.Minute)}
	if got := status(expired, now); got != StatusExpired {
		t.Fatalf("status = %q, want expired", got)
	}
	revokedAt := now.Add(-time.Hour)
	revoked := db.ApiKey{ExpiresAt: now.Add(time.Hour), RevokedAt: &revokedAt}
	if got := status(revoked, now); got != StatusRevoked {
		t.Fatalf("status = %q, want revoked", got)
	}
	// Revocation wins over a live expiry, so a revoked key is never mistaken for
	// one that merely aged out.
	both := db.ApiKey{ExpiresAt: now.Add(-time.Minute), RevokedAt: &revokedAt}
	if got := status(both, now); got != StatusRevoked {
		t.Fatalf("status = %q, want revoked", got)
	}
}

func TestDTONeverCarriesTheSecret(t *testing.T) {
	dto := toDTO(db.ApiKey{
		ID: uuid.New(), OrganizationID: uuid.New(), Name: "CI",
		KeyPrefix: "tfk_abc123xy", KeyLastFour: "wxyz",
		KeyHash: "e3b0c44298fc1c149afbf4c8996fb924", ExpiresAt: time.Now().Add(time.Hour),
	}, time.Now())

	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "e3b0c44298fc1c149afbf4c8996fb924") {
		t.Fatalf("DTO leaked the stored hash: %s", raw)
	}
	if strings.Contains(string(raw), "key_hash") {
		t.Fatalf("DTO exposed the hash field: %s", raw)
	}
	if !strings.Contains(string(raw), `"key_last_four":"wxyz"`) {
		t.Fatalf("the last four characters must remain visible for identification: %s", raw)
	}
}

func TestParseListFilter(t *testing.T) {
	filter, err := parseListFilter(url.Values{"sort": {"expires_at"}, "order": {"asc"}})
	if err != nil {
		t.Fatal(err)
	}
	if filter.Sort != "expires_at" || filter.Desc || filter.IncludeRevoked {
		t.Fatalf("unexpected filter: %+v", filter)
	}
	revoked, err := parseListFilter(url.Values{"include_revoked": {"true"}})
	if err != nil || !revoked.IncludeRevoked {
		t.Fatalf("include_revoked: %+v, %v", revoked, err)
	}
	defaults, err := parseListFilter(url.Values{})
	if err != nil || defaults.Sort != defaultSort || !defaults.Desc || defaults.IncludeRevoked {
		t.Fatalf("unexpected defaults: %+v, %v", defaults, err)
	}
}

func TestParseListFilterRejectsUnsafeInput(t *testing.T) {
	cases := []url.Values{
		{"sort": {"key_hash"}},
		{"sort": {"name; DROP TABLE api_keys"}},
		{"order": {"sideways"}},
		{"include_revoked": {"maybe"}},
	}
	for _, values := range cases {
		if _, err := parseListFilter(values); err == nil {
			t.Fatalf("expected %v to be rejected", values)
		}
	}
}
