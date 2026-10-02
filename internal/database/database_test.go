package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidateRLSRole(t *testing.T) {
	cases := []struct {
		name      string
		superuser bool
		bypassRLS bool
		wantError bool
	}{
		{name: "application role"},
		{name: "superuser", superuser: true, wantError: true},
		{name: "bypass rls", bypassRLS: true, wantError: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := validateRLSRole(test.superuser, test.bypassRLS)
			if (err != nil) != test.wantError {
				t.Fatalf("validateRLSRole() error = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestCheckRLSRoleOnTestDatabase(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database ending in _test")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	if !strings.HasSuffix(poolConfig.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL database name must end in _test")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var superuser, bypassRLS bool
	if err := pool.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&superuser, &bypassRLS); err != nil {
		t.Fatal(err)
	}
	err = (&Pool{Pool: pool}).CheckRLSRole(ctx)
	if (superuser || bypassRLS) && err == nil {
		t.Fatal("CheckRLSRole accepted a superuser or BYPASSRLS role")
	}
	if !superuser && !bypassRLS && err != nil {
		t.Fatalf("CheckRLSRole rejected a constrained role: %v", err)
	}
}
