package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// testPool requires an explicit disposable TEST_DATABASE_URL with a database
// name ending in _test. Integration tests are skipped when it is unset.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a disposable database ending in _test")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	if !strings.HasSuffix(config.ConnConfig.Database, "_test") {
		t.Fatal("TEST_DATABASE_URL database name must end in _test; refusing to truncate")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// Start each test from a clean slate. CASCADE clears dependent rows.
	_, err = pool.Exec(ctx, `TRUNCATE refresh_tokens, organization_memberships, roles, organizations, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func newService(t *testing.T, pool *pgxpool.Pool) *auth.Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("integration-secret", "teamflow", 15*time.Minute)
	return auth.NewService(pool, jwt, 720*time.Hour, logger)
}

func sampleRegister() auth.RegisterInput {
	return auth.RegisterInput{
		Email:            "owner@acme.test",
		Password:         "StrongPassword123",
		FirstName:        "Olivia",
		LastName:         "Owner",
		OrganizationName: "Acme Inc",
	}
}

func TestIntegration_RegisterLoginRefresh(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool)
	ctx := context.Background()
	meta := auth.RequestMeta{UserAgent: "test", IPAddress: "127.0.0.1"}

	// Register
	res, err := svc.Register(ctx, sampleRegister(), meta)
	if err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if res.Tokens.AccessToken == "" || res.Tokens.RefreshToken == "" {
		t.Fatal("expected tokens from register")
	}
	if res.User.Email != "owner@acme.test" {
		t.Errorf("email = %q", res.User.Email)
	}

	// Duplicate registration must be rejected.
	if _, err := svc.Register(ctx, sampleRegister(), meta); err == nil {
		t.Fatal("expected conflict on duplicate email")
	}

	// Login with correct credentials.
	loginRes, err := svc.Login(ctx, auth.LoginInput{Email: "owner@acme.test", Password: "StrongPassword123"}, meta)
	if err != nil {
		t.Fatalf("Login error = %v", err)
	}
	if loginRes.Tokens.RefreshToken == res.Tokens.RefreshToken {
		t.Error("login should issue a distinct refresh token")
	}

	// Login with wrong password must fail.
	if _, err := svc.Login(ctx, auth.LoginInput{Email: "owner@acme.test", Password: "nope"}, meta); err == nil {
		t.Fatal("expected error for wrong password")
	}

	// Refresh rotates the token.
	refreshed, err := svc.Refresh(ctx, loginRes.Tokens.RefreshToken, meta)
	if err != nil {
		t.Fatalf("Refresh error = %v", err)
	}
	if refreshed.Tokens.RefreshToken == loginRes.Tokens.RefreshToken {
		t.Error("refresh should rotate the refresh token")
	}

	// Reuse of the now-rotated token is detected and rejected.
	if _, err := svc.Refresh(ctx, loginRes.Tokens.RefreshToken, meta); err == nil {
		t.Fatal("expected reuse detection to reject the old token")
	}

	// After reuse detection the whole family is revoked, so the rotated token
	// also stops working.
	if _, err := svc.Refresh(ctx, refreshed.Tokens.RefreshToken, meta); err == nil {
		t.Fatal("expected family revocation to reject the rotated token")
	}
}

func TestIntegration_LogoutAll(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool)
	ctx := context.Background()
	meta := auth.RequestMeta{}

	res, err := svc.Register(ctx, sampleRegister(), meta)
	if err != nil {
		t.Fatalf("Register error = %v", err)
	}

	// A second session for the same user.
	second, err := svc.Login(ctx, auth.LoginInput{Email: "owner@acme.test", Password: "StrongPassword123"}, meta)
	if err != nil {
		t.Fatalf("Login error = %v", err)
	}

	if err := svc.LogoutAll(ctx, res.User.ID); err != nil {
		t.Fatalf("LogoutAll error = %v", err)
	}

	// Both sessions' refresh tokens must now be rejected.
	if _, err := svc.Refresh(ctx, res.Tokens.RefreshToken, meta); err == nil {
		t.Error("expected first session refresh to fail after logout-all")
	}
	if _, err := svc.Refresh(ctx, second.Tokens.RefreshToken, meta); err == nil {
		t.Error("expected second session refresh to fail after logout-all")
	}
}

func TestIntegration_ConcurrentRefresh(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	registered, err := svc.Register(ctx, sampleRegister(), auth.RequestMeta{})
	if err != nil {
		t.Fatalf("Register error = %v", err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `SELECT id FROM refresh_tokens WHERE user_id = $1 FOR UPDATE`, registered.User.ID); err != nil {
		t.Fatal(err)
	}

	type refreshOutcome struct {
		result *auth.AuthResult
		err    error
	}
	outcomes := make(chan refreshOutcome, 2)
	for range 2 {
		go func() {
			result, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, auth.RequestMeta{})
			outcomes <- refreshOutcome{result: result, err: err}
		}()
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			AND query LIKE '%refresh_tokens%' AND pid <> pg_backend_pid()`).Scan(&waiting)
		if err != nil {
			t.Fatalf("wait for competing refresh requests: %v", err)
		}
		if waiting == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("refresh requests did not both block on the token row")
		case <-ticker.C:
		}
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var winner *auth.AuthResult
	successes, rejected := 0, 0
	for range 2 {
		select {
		case outcome := <-outcomes:
			if outcome.err == nil {
				successes++
				winner = outcome.result
			} else if errors.Is(outcome.err, httpx.ErrUnauthorized) {
				rejected++
			} else {
				t.Fatalf("unexpected refresh error: %v", outcome.err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent refresh requests did not finish")
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("got %d successes and %d unauthorized responses, want one each", successes, rejected)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, registered.User.ID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("reuse left %d active refresh tokens", active)
	}
	if _, err := svc.Refresh(ctx, winner.Tokens.RefreshToken, auth.RequestMeta{}); !errors.Is(err, httpx.ErrUnauthorized) {
		t.Fatalf("replacement token after reuse: got %v, want unauthorized", err)
	}
}

func TestIntegration_Logout(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool)
	ctx := context.Background()
	meta := auth.RequestMeta{}

	res, err := svc.Register(ctx, sampleRegister(), meta)
	if err != nil {
		t.Fatalf("Register error = %v", err)
	}

	if err := svc.Logout(ctx, res.Tokens.RefreshToken); err != nil {
		t.Fatalf("Logout error = %v", err)
	}
	if _, err := svc.Refresh(ctx, res.Tokens.RefreshToken, meta); err == nil {
		t.Error("expected refresh to fail after logout")
	}

	// Logging out with an unknown token is a no-op, not an error.
	if err := svc.Logout(ctx, "unknown-token"); err != nil {
		t.Errorf("Logout with unknown token error = %v", err)
	}
}
