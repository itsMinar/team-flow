package organizations_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/apikeys"
	"github.com/itsMinar/team-flow/internal/audit"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/organizations"
)

// auditSetup builds the services with a real audit recorder so the tests assert
// on stored rows rather than on recorder calls.
func auditSetup(t *testing.T) (*audit.Service, audit.Recorder, *pgxpool.Pool, *organizations.Service, string) {
	t.Helper()
	pool := testPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recorder := audit.NewSQLRecorder(db.New(pool), nil, logger)
	orgSvc := organizations.NewService(pool, logger)
	return audit.NewService(pool, orgSvc), recorder, pool, orgSvc, "integration-secret"
}

func TestAuditLogRecordsSecurityEvents(t *testing.T) {
	ctx := context.Background()
	auditSvc, recorder, pool, orgSvc, secret := auditSetup(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService(secret, "teamflow", 15*time.Minute)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, recorder, logger)
	inviteSvc := invitations.NewService(pool, orgSvc, authSvc, &recordingSender{}, nil, recorder,
		7*24*time.Hour, "http://app.example.com", logger)
	keySvc := apikeys.NewService(pool, orgSvc, recorder, 90*24*time.Hour, 365*24*time.Hour, logger)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userID, orgID := registerOrg(t, pool, "audit-owner@example.com", "Audit Org")

	// A successful and a failed login are both recorded, and the failure is
	// recorded without inventing an actor.
	if _, err := authSvc.Login(ctx, auth.LoginInput{
		Email: "audit-owner@example.com", Password: "StrongPassword123",
	}, auth.RequestMeta{IPAddress: "203.0.113.5", UserAgent: "audit-test"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := authSvc.Login(ctx, auth.LoginInput{
		Email: "audit-owner@example.com", Password: "WrongPassword1",
	}, auth.RequestMeta{IPAddress: "203.0.113.6"}); err == nil {
		t.Fatal("expected the login to fail")
	}
	if _, err := authSvc.Login(ctx, auth.LoginInput{
		Email: "nobody@example.com", Password: "StrongPassword123",
	}, auth.RequestMeta{IPAddress: "203.0.113.7"}); err == nil {
		t.Fatal("expected the login to fail")
	}

	// Credentials and invitations are recorded as they are minted, inside the
	// transaction that creates them.
	key, err := keySvc.Create(ctx, userID, orgID, apikeys.CreateInput{Name: "CI"})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := keySvc.Revoke(ctx, userID, orgID, key.APIKey.ID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	if _, err := inviteSvc.Create(ctx, userID, orgID,
		invitations.CreateInput{Email: "audit-invitee@example.com", RoleID: roleIDOf(t, pool, orgID, "Member")}); err != nil {
		t.Fatalf("invite: %v", err)
	}

	// Authentication events belong to no organization, so they are recorded for the
	// operator but are invisible through any tenant. That separation is the point:
	// a failed login must be reviewable without being readable by a customer.
	entries, pagination, err := auditSvc.List(ctx, userID, orgID, audit.ListFilter{}, firstPage)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	actions := map[string]audit.Entry{}
	for _, entry := range entries {
		actions[entry.Action] = entry
	}
	for _, action := range []string{audit.APIKeyCreated, audit.APIKeyRevoked, audit.InvitationCreated} {
		if _, ok := actions[action]; !ok {
			t.Fatalf("missing tenant audit action %q in %v", action, actionNames(entries))
		}
	}
	for _, action := range []string{audit.AuthLoginSucceeded, audit.AuthLoginFailed} {
		if _, ok := actions[action]; ok {
			t.Fatalf("%q must not be visible through a tenant", action)
		}
	}
	if pagination.Total != int64(len(entries)) {
		t.Fatalf("pagination total = %d, entries = %d", pagination.Total, len(entries))
	}

	// Authentication events are readable by an operator query instead.
	successes := readGlobalAudit(t, pool, audit.AuthLoginSucceeded)
	if len(successes) != 1 {
		t.Fatalf("expected one recorded login event, got %d", len(successes))
	}
	success := successes[0]
	if success.IPAddress == nil || *success.IPAddress != "203.0.113.5" ||
		success.UserAgent == nil || *success.UserAgent != "audit-test" {
		t.Fatalf("login provenance is missing: %+v", success)
	}
	if success.ActorUserID == nil || *success.ActorUserID != userID {
		t.Fatalf("actor mismatch: %+v", success)
	}
	// A failed login is recorded without the password or the attempted address.
	failures := readGlobalAudit(t, pool, audit.AuthLoginFailed)
	if len(failures) != 2 {
		t.Fatalf("expected two login failures, got %d", len(failures))
	}
	for _, failure := range failures {
		if failure.Outcome != audit.OutcomeFailure {
			t.Fatalf("failure outcome = %q", failure.Outcome)
		}
		raw, err := json.Marshal(failure.Metadata)
		if err != nil {
			t.Fatal(err)
		}
		for _, secretText := range []string{"WrongPassword1", "audit-owner@example.com", "nobody@example.com"} {
			if strings.Contains(string(raw), secretText) {
				t.Fatalf("audit metadata leaked %q: %s", secretText, raw)
			}
		}
	}
	// The minted credential is recorded by identity only, never by secret.
	createdRaw, err := json.Marshal(actions[audit.APIKeyCreated].Metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(createdRaw), key.Key) {
		t.Fatalf("audit metadata leaked the api key: %s", createdRaw)
	}
}

// readGlobalAudit reads organization-less audit rows the way an operator would,
// bypassing the tenant-scoped API on purpose.
func readGlobalAudit(t *testing.T, pool *pgxpool.Pool, action string) []audit.Entry {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id, action, outcome, actor_user_id, host(ip_address), user_agent, request_id, trace_id, metadata, created_at
		 FROM audit_logs WHERE action = $1 AND organization_id IS NULL ORDER BY created_at`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []audit.Entry
	for rows.Next() {
		var (
			entry audit.Entry
			actor *uuid.UUID
			ip    *string
			agent *string
			reqID *string
			trace *string
			meta  []byte
		)
		if err := rows.Scan(&entry.ID, &entry.Action, &entry.Outcome, &actor, &ip, &agent, &reqID, &trace, &meta, &entry.CreatedAt); err != nil {
			t.Fatal(err)
		}
		entry.ActorUserID = actor
		entry.IPAddress = ip
		entry.UserAgent = agent
		entry.RequestID = reqID
		entry.TraceID = trace
		entry.Metadata = meta
		out = append(out, entry)
	}
	return out
}

func TestAuditLogIsAppendOnlyAndTenantScoped(t *testing.T) {
	ctx := context.Background()
	auditSvc, recorder, pool, _, secret := auditSetup(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService(secret, "teamflow", 15*time.Minute)
	_ = auth.NewService(pool, jwt, 720*time.Hour, audit.NopRecorder(), logger)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userA, orgA := registerOrg(t, pool, "audit-a@example.com", "Audit Org A")
	userB, orgB := registerOrg(t, pool, "audit-b@example.com", "Audit Org B")
	recorder.Record(ctx, audit.Event{
		Action: audit.OrganizationCreated, OrganizationID: &orgA, ActorUserID: &userA,
	})

	mine, _, err := auditSvc.List(ctx, userA, orgA, audit.ListFilter{}, firstPage)
	if err != nil || len(mine) != 1 {
		t.Fatalf("own audit log: %d entries, %v", len(mine), err)
	}
	// Another tenant sees nothing.
	theirs, _, err := auditSvc.List(ctx, userB, orgB, audit.ListFilter{}, firstPage)
	if err != nil || len(theirs) != 0 {
		t.Fatalf("org B read org A's audit log: %d entries, %v", len(theirs), err)
	}
	// A cross-tenant read is a 404 rather than a leak.
	if _, _, err := auditSvc.List(ctx, userA, orgB, audit.ListFilter{}, firstPage); err == nil {
		t.Fatal("expected a cross-tenant audit read to fail")
	} else {
		requireAPIError(t, err, 404, "ORGANIZATION_NOT_FOUND")
	}
	// Even a valid membership in another tenant cannot be pointed at this log.
	if _, _, err := auditSvc.List(ctx, userB, orgA, audit.ListFilter{}, firstPage); err == nil {
		t.Fatal("a non-member must not read the audit log")
	}

	// Reading requires audit.read, which only Owner and Admin have.
	viewer := joinMember(t, pool, orgA, "audit-viewer@example.com", "Viewer")
	if _, _, err := auditSvc.List(ctx, viewer, orgA, audit.ListFilter{}, firstPage); err == nil {
		t.Fatal("a viewer must not read the audit log")
	} else {
		requireAPIError(t, err, 403, "FORBIDDEN")
	}
	admin := joinMember(t, pool, orgA, "audit-admin@example.com", "Admin")
	if _, _, err := auditSvc.List(ctx, admin, orgA, audit.ListFilter{}, firstPage); err != nil {
		t.Fatalf("an admin must read the audit log: %v", err)
	}

	// The application role may insert and read, but never rewrite history. The
	// check only applies when connected as that role: a superuser or the table
	// owner bypasses privileges.
	if !bypassesRLS(t, pool) {
		var pgErr *pgconn.PgError
		if _, err := pool.Exec(ctx, `UPDATE audit_logs SET action = 'tampered'`); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected audit updates to be denied, got %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM audit_logs`); !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected audit deletes to be denied, got %v", err)
		}
	}

	// Filtering narrows by action and outcome.
	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_logs (organization_id, action, outcome) VALUES ($1, 'apikey.created', 'success')`,
		orgA); err != nil {
		t.Fatal(err)
	}
	action, outcome := audit.APIKeyCreated, audit.OutcomeSuccess
	filtered, pagination, err := auditSvc.List(ctx, userA, orgA, audit.ListFilter{
		Action: &action, Outcome: &outcome,
	}, firstPage)
	if err != nil || pagination.Total != 1 || len(filtered) != 1 {
		t.Fatalf("filter by action and outcome: %+v, %v", pagination, err)
	}
	missing, _, err := auditSvc.List(ctx, userA, orgA, audit.ListFilter{Action: &outcome}, firstPage)
	if err != nil || len(missing) != 0 {
		t.Fatalf("unexpected filter match: %d, %v", len(missing), err)
	}
}

func TestAuditRecorderNeverFailsTheCaller(t *testing.T) {
	ctx := context.Background()
	_, _, pool, _, _ := auditSetup(t)
	recorder := audit.NewSQLRecorder(db.New(pool), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// An event with no action is rejected by the recorder instead of writing a
	// meaningless row, and the caller is unaffected.
	recorder.Record(ctx, audit.Event{Outcome: audit.OutcomeSuccess})
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("an invalid event must not be stored, found %d rows", stored)
	}

	// A valid event is stored, and a cancelled context is reported rather than
	// silently swallowed.
	recorder.Record(ctx, audit.Event{
		Action: audit.APIKeyCreated, Metadata: map[string]any{"name": "x"},
	})
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("stored rows = %d, want 1", stored)
	}
}

func actionNames(entries []audit.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Action)
	}
	return out
}
