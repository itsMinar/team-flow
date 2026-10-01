package organizations_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
)

func TestAPIKeysOneTimeDisplayAuthenticationAndRevocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	projectSvc := projects.NewService(pool, orgSvc)
	svc := apikeys.NewService(pool, orgSvc, audit.NopRecorder(), 90*24*time.Hour, 365*24*time.Hour, logger)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userA, orgA := registerOrg(t, pool, "key-a@example.com", "API Keys Org A")
	userB, orgB := registerOrg(t, pool, "key-b@example.com", "API Keys Org B")
	if _, err := projectSvc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Website Redesign"}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Run("creation returns the secret exactly once", func(t *testing.T) {
		created, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "  CI pipeline  "})
		if err != nil {
			t.Fatalf("create api key: %v", err)
		}
		if !strings.HasPrefix(created.Key, "tfk_") {
			t.Fatalf("unexpected key format: %q", created.Key)
		}
		if created.Warning == "" || created.APIKey.Name != "CI pipeline" ||
			created.APIKey.Status != apikeys.StatusActive || created.APIKey.CreatedBy != userA {
			t.Fatalf("unexpected creation result: %+v", created)
		}
		if created.APIKey.KeyLastFour != created.Key[len(created.Key)-4:] {
			t.Fatalf("last four characters mismatch: %+v", created.APIKey)
		}
		// The secret is not recoverable from the database.
		var stored string
		if err := pool.QueryRow(ctx, `SELECT key_hash FROM api_keys WHERE id = $1`, created.APIKey.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored == created.Key || len(stored) != 64 {
			t.Fatalf("the stored value must be a hash, got %q", stored)
		}
		// Listing shows identifiers only.
		listed, pag, err := svc.List(ctx, userA, orgA, apikeys.ListFilter{}, firstPage)
		if err != nil || pag.Total != 1 || len(listed) != 1 {
			t.Fatalf("list: %+v, %v", pag, err)
		}
		if listed[0].KeyPrefix != created.APIKey.KeyPrefix || listed[0].KeyLastFour != created.APIKey.KeyLastFour {
			t.Fatalf("identifiers changed between create and list: %+v", listed[0])
		}
		raw := mustMarshal(t, listed[0])
		if containsAny(raw, created.Key, stored) {
			t.Fatalf("listing leaked the secret or its hash: %s", raw)
		}
		// Input validation.
		_, err = svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "  "})
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
		_, err = svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Long", ExpiresInDays: 400})
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
		// An explicit lifetime shorter than the default is allowed.
		short, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Short", ExpiresInDays: 7})
		if err != nil {
			t.Fatalf("create short-lived key: %v", err)
		}
		if short.APIKey.ExpiresAt.After(time.Now().Add(8 * 24 * time.Hour)) {
			t.Fatalf("unexpected expiry: %v", short.APIKey.ExpiresAt)
		}
	})

	t.Run("a key authenticates as its creator", func(t *testing.T) {
		created, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Agent"})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := svc.Authenticate(ctx, created.Key)
		if err != nil {
			t.Fatalf("authenticate: %v", err)
		}
		if principal.UserID != userA || !principal.IsAPIKey() ||
			principal.APIKeyID != created.APIKey.ID || principal.OrganizationID != orgA ||
			principal.Method != authctx.MethodAPIKey {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		// The key carries no authorization state: the creator's live membership is
		// what grants access, so a project read resolves normally.
		if _, err := orgSvc.Authorize(ctx, principal.UserID, principal.OrganizationID, "projects.read"); err != nil {
			t.Fatalf("key principal cannot act in its organization: %v", err)
		}
		// Usage is recorded, and it does not break authentication.
		var lastUsed *time.Time
		if err := pool.QueryRow(ctx, `SELECT last_used_at FROM api_keys WHERE id = $1`, created.APIKey.ID).Scan(&lastUsed); err != nil {
			t.Fatal(err)
		}
		if lastUsed == nil {
			t.Fatal("usage was not recorded")
		}

		// Junk is rejected before it reaches the index.
		for _, raw := range []string{"", "tfk_short", "not-a-key", created.Key + "x", created.Key[:len(created.Key)-1]} {
			if _, err := svc.Authenticate(ctx, raw); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			} else {
				apiErr := httpx.FromError(err)
				if apiErr.Status != 401 || apiErr.Code != "INVALID_API_KEY" {
					t.Fatalf("unexpected error for %q: %d %s", raw, apiErr.Status, apiErr.Code)
				}
			}
		}
	})

	t.Run("role changes take effect immediately", func(t *testing.T) {
		// Minting keys requires api_keys.manage, so the creator starts as an
		// Admin; the key's power is then shown to follow the live role.
		user := joinMember(t, pool, orgA, "key-role@example.com", "Admin")
		created, err := svc.Create(ctx, user, orgA, apikeys.CreateInput{Name: "Admin agent"})
		if err != nil {
			t.Fatalf("create api key: %v", err)
		}
		principal, err := svc.Authenticate(ctx, created.Key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orgSvc.Authorize(ctx, principal.UserID, principal.OrganizationID, "projects.create"); err != nil {
			t.Fatalf("an admin key must create projects: %v", err)
		}
		// Demote the creator; the same key immediately loses the permission.
		setRole(t, pool, orgA, user, "Viewer")
		principal, err = svc.Authenticate(ctx, created.Key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := orgSvc.Authorize(ctx, principal.UserID, principal.OrganizationID, "projects.create"); err == nil {
			t.Fatal("a key must not keep permissions after the creator is demoted")
		}
	})

	t.Run("removing the membership revokes the key", func(t *testing.T) {
		user := joinMember(t, pool, orgA, "key-leaving@example.com", "Admin")
		created, err := svc.Create(ctx, user, orgA, apikeys.CreateInput{Name: "Leaving agent"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authenticate(ctx, created.Key); err != nil {
			t.Fatalf("authenticate before removal: %v", err)
		}
		removeMembership(t, pool, orgA, user)
		// The composite foreign key removes the key with the membership, so a
		// credential can never outlive the membership that backs it.
		var remaining int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE id = $1`, created.APIKey.ID).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("key survived membership removal: %d, %v", remaining, err)
		}
		_, err = svc.Authenticate(ctx, created.Key)
		requireAPIError(t, err, 401, "INVALID_API_KEY")
	})

	t.Run("expiry and revocation are enforced", func(t *testing.T) {
		created, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Expiring"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Authenticate(ctx, created.Key); err != nil {
			t.Fatalf("authenticate before expiry: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE api_keys SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day'
			WHERE id = $1`, created.APIKey.ID); err != nil {
			t.Fatal(err)
		}
		_, err = svc.Authenticate(ctx, created.Key)
		requireAPIError(t, err, 401, "INVALID_API_KEY")
		// The list reports the derived status without writing to the database.
		expired, pag, err := svc.List(ctx, userA, orgA, apikeys.ListFilter{IncludeRevoked: true}, firstPage)
		if err != nil {
			t.Fatal(err)
		}
		if pag.Total == 0 || !hasKeyStatus(expired, created.APIKey.ID, apikeys.StatusExpired) {
			t.Fatalf("list must report the expired key: %+v", expired)
		}

		revokeMe, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Revoke me"})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Revoke(ctx, userA, orgA, revokeMe.APIKey.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		_, err = svc.Authenticate(ctx, revokeMe.Key)
		requireAPIError(t, err, 401, "INVALID_API_KEY")
		// Revoking twice is a safe 404 and cannot be used to probe history.
		requireAPIError(t, svc.Revoke(ctx, userA, orgA, revokeMe.APIKey.ID), 404, "API_KEY_NOT_FOUND")

		// Revoked keys are hidden unless explicitly requested.
		active, pag, err := svc.List(ctx, userA, orgA, apikeys.ListFilter{}, firstPage)
		if err != nil {
			t.Fatal(err)
		}
		if hasKeyStatus(active, revokeMe.APIKey.ID, apikeys.StatusRevoked) {
			t.Fatalf("revoked keys must be hidden by default: %+v", active)
		}
		if _, _, err = svc.List(ctx, userA, orgA, apikeys.ListFilter{IncludeRevoked: true}, firstPage); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("keys never cross tenants", func(t *testing.T) {
		created, err := svc.Create(ctx, userA, orgA, apikeys.CreateInput{Name: "Scoped"})
		if err != nil {
			t.Fatal(err)
		}
		// Org B sees none of org A's keys.
		_, pag, err := svc.List(ctx, userB, orgB, apikeys.ListFilter{IncludeRevoked: true}, firstPage)
		if err != nil || pag.Total != 0 {
			t.Fatalf("org B sees org A keys: %+v, %v", pag, err)
		}
		// Cross-tenant access to a specific key is a safe 404.
		requireAPIError(t, svc.Revoke(ctx, userB, orgB, created.APIKey.ID), 404, "API_KEY_NOT_FOUND")
		_, _, err = svc.List(ctx, userB, orgA, apikeys.ListFilter{}, firstPage)
		requireAPIError(t, err, 404, "ORGANIZATION_NOT_FOUND")
		// A key authenticates as its creator in its own organization only.
		principal, err := svc.Authenticate(ctx, created.Key)
		if err != nil {
			t.Fatal(err)
		}
		if principal.OrganizationID == orgB {
			t.Fatal("the key must be pinned to its own organization")
		}
		if _, err := orgSvc.ResolveTenant(ctx, principal.UserID, orgB); err == nil {
			t.Fatal("a key must not grant access to another organization")
		}
	})

	t.Run("only holders of api_keys.manage can mint keys", func(t *testing.T) {
		viewer := joinMember(t, pool, orgA, "key-viewer@example.com", "Viewer")
		_, err := svc.Create(ctx, viewer, orgA, apikeys.CreateInput{Name: "Nope"})
		requireAPIError(t, err, 403, "FORBIDDEN")
		_, _, err = svc.List(ctx, viewer, orgA, apikeys.ListFilter{}, firstPage)
		requireAPIError(t, err, 403, "FORBIDDEN")
		manager := joinMember(t, pool, orgA, "key-manager@example.com", "Manager")
		_, err = svc.Create(ctx, manager, orgA, apikeys.CreateInput{Name: "Nope"})
		requireAPIError(t, err, 403, "FORBIDDEN")
		admin := joinMember(t, pool, orgA, "key-admin@example.com", "Admin")
		if _, err := svc.Create(ctx, admin, orgA, apikeys.CreateInput{Name: "CI"}); err != nil {
			t.Fatalf("an admin must be able to mint keys: %v", err)
		}
		// An Admin key manages keys, matching the live role.
		adminKey, err := svc.Create(ctx, admin, orgA, apikeys.CreateInput{Name: "Admin agent"})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := svc.Authenticate(ctx, adminKey.Key)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.List(ctx, principal.UserID, principal.OrganizationID, apikeys.ListFilter{}, firstPage); err != nil {
			t.Fatalf("an admin key must list keys: %v", err)
		}
	})

	t.Run("listing filters, sorts, and paginates", func(t *testing.T) {
		items, pag, err := svc.List(ctx, userA, orgA, apikeys.ListFilter{}, httpx.PageRequest{Page: 1, PageSize: 2})
		if err != nil || pag.PageSize != 2 || pag.Total < 3 || pag.TotalPages < 2 || len(items) != 2 {
			t.Fatalf("pagination: %+v, %v", pag, err)
		}
		byExpiry, _, err := svc.List(ctx, userA, orgA,
			apikeys.ListFilter{Sort: "expires_at", IncludeRevoked: true}, firstPage)
		if err != nil || len(byExpiry) < 2 {
			t.Fatalf("sort by expiry: %+v, %v", byExpiry, err)
		}
		for i := 1; i < len(byExpiry); i++ {
			if byExpiry[i].ExpiresAt.Before(byExpiry[i-1].ExpiresAt) {
				t.Fatalf("expiry sort is not ascending: %+v", byExpiry)
			}
		}
	})

	t.Run("database constraints hold", func(t *testing.T) {
		var pgErr *pgconn.PgError
		// The creator must belong to the organization.
		if _, err := pool.Exec(ctx, `
			INSERT INTO api_keys (organization_id, created_by, name, key_prefix, key_last_four, key_hash, expires_at)
			VALUES ($1, $2, 'Cross tenant', 'tfk_abcdefgh', 'wxyz', 'hash-cross-tenant', now() + interval '1 day')`,
			orgA, userB); !errors.As(err, &pgErr) || pgErr.Code != "23503" ||
			pgErr.ConstraintName != "api_keys_creator_organization_fkey" {
			t.Fatalf("expected a creator foreign key violation, got %v", err)
		}
		// Hashes are unique, so two keys can never share a secret.
		if _, err := pool.Exec(ctx, `
			INSERT INTO api_keys (organization_id, created_by, name, key_prefix, key_last_four, key_hash, expires_at)
			SELECT organization_id, created_by, name, key_prefix, key_last_four, key_hash, now() + interval '1 day'
			FROM api_keys LIMIT 1`); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("expected a unique hash violation, got %v", err)
		}
		// Public identifiers must be exactly four characters for the last four.
		if _, err := pool.Exec(ctx, `
			INSERT INTO api_keys (organization_id, created_by, name, key_prefix, key_last_four, key_hash, expires_at)
			VALUES ($1, $2, 'Bad last four', 'tfk_abcdefgh', 'toolong', 'hash-bad-last-four', now() + interval '1 day')`,
			orgA, userA); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected a check violation, got %v", err)
		}
	})

	t.Run("RLS isolates api keys", func(t *testing.T) {
		if bypassesRLS(t, pool) {
			t.Skip("connected role bypasses RLS; run with the teamflow_app role to verify")
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", orgB.String()); err != nil {
			t.Fatal(err)
		}
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE organization_id = $1`, orgA).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("RLS leaked org A keys into org B context: %d, %v", visible, err)
		}
	})

}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func hasKeyStatus(keys []apikeys.APIKeyDTO, id uuid.UUID, status string) bool {
	for _, key := range keys {
		if key.ID == id && key.Status == status {
			return true
		}
	}
	return false
}
