package organizations_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/organizations"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database ending in _test")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL database name must end in _test")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(context.Background(), `TRUNCATE refresh_tokens, organization_memberships, roles, organizations, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func registerOrg(t *testing.T, pool *pgxpool.Pool, email, orgName string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("integration-secret", "teamflow", 900000000000)
	asvc := auth.NewService(pool, jwt, 720*3600000000000, logger)
	res, err := asvc.Register(context.Background(), auth.RegisterInput{
		Email: email, Password: "StrongPassword123", FirstName: "T", LastName: "U", OrganizationName: orgName,
	}, auth.RequestMeta{})
	if err != nil {
		t.Fatalf("register %s: %v", email, err)
	}
	// Find org via service list.
	osvc := organizations.NewService(pool, logger)
	mine, err := osvc.ListMyOrganizations(context.Background(), res.User.ID)
	if err != nil || len(mine) == 0 {
		t.Fatalf("list mine: %v %v", mine, err)
	}
	return res.User.ID, mine[0].ID
}

func TestCrossTenantIsolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := organizations.NewService(pool, logger)

	userA, orgA := registerOrg(t, pool, "iso-a@example.com", "Iso Org A")
	userB, orgB := registerOrg(t, pool, "iso-b@example.com", "Iso Org B")

	if _, err := svc.GetOrganization(ctx, userA, orgB); err == nil {
		t.Fatal("expected cross-tenant read to fail")
	}
	if _, err := svc.ListMembers(ctx, userA, orgB); err == nil {
		t.Fatal("expected cross-tenant members list to fail")
	}
	if _, err := svc.UpdateOrganization(ctx, userA, orgB, "Hacked"); err == nil {
		t.Fatal("expected cross-tenant update to fail")
	}
	got, err := svc.GetOrganization(ctx, userA, orgA)
	if err != nil {
		t.Fatalf("own org read: %v", err)
	}
	if got.Role != "Owner" {
		t.Fatalf("role = %q, want Owner", got.Role)
	}
	roles, err := svc.ListRoles(ctx, userA, orgA)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 5 {
		t.Fatalf("role count = %d, want 5", len(roles))
	}
	if len(roles[0].Permissions) == 0 {
		t.Fatal("expected seeded role permissions")
	}
	custom, err := svc.CreateRole(ctx, userA, orgA, "Project Lead", nil, []string{"organizations.read", "members.read"})
	if err != nil {
		t.Fatalf("create custom role: %v", err)
	}
	if err := svc.DeleteRole(ctx, userA, orgA, custom.ID); err != nil {
		t.Fatalf("delete custom role: %v", err)
	}
	mine, err := svc.ListMyOrganizations(ctx, userA)
	if err != nil {
		t.Fatalf("list mine: %v", err)
	}
	for _, o := range mine {
		if o.ID == orgB {
			t.Fatal("org B leaked into org A list")
		}
	}
	// Org B owner can still access own org.
	if _, err := svc.GetOrganization(ctx, userB, orgB); err != nil {
		t.Fatalf("org B owner read: %v", err)
	}
}
