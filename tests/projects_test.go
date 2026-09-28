package organizations_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/teams"
)

func requireAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %d %s, got nil", status, code)
	}
	apiErr := httpx.FromError(err)
	if apiErr.Status != status || (code != "" && apiErr.Code != code) {
		t.Fatalf("expected %d %s, got %d %s (%v)", status, code, apiErr.Status, apiErr.Code, err)
	}
}

func decodeUpdate(t *testing.T, body string) projects.UpdateInput {
	t.Helper()
	var in projects.UpdateInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return in
}

func joinOrg(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, roleName string) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	role, err := q.GetRoleByName(ctx, db.GetRoleByNameParams{OrganizationID: orgID, Name: roleName})
	if err != nil {
		t.Fatalf("get role %s: %v", roleName, err)
	}
	now := time.Now()
	if _, err := q.CreateMembership(ctx, db.CreateMembershipParams{
		OrganizationID: orgID, UserID: userID, RoleID: role.ID, Status: "active", JoinedAt: &now,
	}); err != nil {
		t.Fatalf("join org as %s: %v", roleName, err)
	}
}

func setRole(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, roleName string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE organization_memberships
		SET role_id = (SELECT id FROM roles WHERE organization_id = $1 AND name = $3)
		WHERE organization_id = $1 AND user_id = $2`, orgID, userID, roleName)
	if err != nil {
		t.Fatalf("set role %s: %v", roleName, err)
	}
}

func bypassesRLS(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var bypass bool
	if err := pool.QueryRow(context.Background(),
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass); err != nil {
		t.Fatal(err)
	}
	return bypass
}

func TestProjectsTenantIsolationRBACAndActivity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	teamSvc := teams.NewService(pool, orgSvc, logger)
	svc := projects.NewService(pool, orgSvc)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userA, orgA := registerOrg(t, pool, "proj-a@example.com", "Projects Org A")
	userB, orgB := registerOrg(t, pool, "proj-b@example.com", "Projects Org B")

	// Registered owners must receive team and project permissions.
	teamA, err := teamSvc.Create(ctx, userA, orgA, "Engineering", nil)
	if err != nil {
		t.Fatalf("registered owner create team: %v", err)
	}
	teamB, err := teamSvc.Create(ctx, userB, orgB, "Design", nil)
	if err != nil {
		t.Fatalf("create team B: %v", err)
	}

	_, err = svc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Leak", TeamID: &teamB.ID})
	requireAPIError(t, err, 422, "INVALID_TEAM")

	p1, err := svc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Website Redesign", TeamID: &teamA.ID, Status: "active", Priority: "high"})
	if err != nil {
		t.Fatalf("create p1: %v", err)
	}
	p2, err := svc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Mobile App", Status: "active", Priority: "urgent"})
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}
	p3, err := svc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Internal Platform", Priority: "low"})
	if err != nil {
		t.Fatalf("create p3: %v", err)
	}
	if p3.Status != "planning" || p1.CreatedBy != userA {
		t.Fatalf("unexpected defaults: %+v %+v", p3, p1)
	}

	t.Run("filtering, sorting, and pagination", func(t *testing.T) {
		active := "active"
		items, pag, err := svc.List(ctx, userA, orgA, projects.ListFilter{Status: &active, Sort: "created_at"}, firstPage)
		if err != nil || pag.Total != 2 || len(items) != 2 {
			t.Fatalf("status filter: %d items, %+v, %v", len(items), pag, err)
		}
		items, pag, err = svc.List(ctx, userA, orgA, projects.ListFilter{TeamID: &teamA.ID}, firstPage)
		if err != nil || pag.Total != 1 || items[0].ID != p1.ID {
			t.Fatalf("team filter: %+v, %+v, %v", items, pag, err)
		}
		byPriority := projects.ListFilter{Sort: "priority", Desc: true}
		items, pag, err = svc.List(ctx, userA, orgA, byPriority, httpx.PageRequest{Page: 1, PageSize: 2})
		if err != nil || pag.Total != 3 || pag.TotalPages != 2 || len(items) != 2 ||
			items[0].ID != p2.ID || items[1].ID != p1.ID {
			t.Fatalf("priority page 1: %+v, %+v, %v", items, pag, err)
		}
		items, _, err = svc.List(ctx, userA, orgA, byPriority, httpx.PageRequest{Page: 2, PageSize: 2})
		if err != nil || len(items) != 1 || items[0].ID != p3.ID {
			t.Fatalf("priority page 2: %+v, %v", items, err)
		}
		items, _, err = svc.List(ctx, userA, orgA, projects.ListFilter{Sort: "name"}, firstPage)
		if err != nil || items[0].Name != "Internal Platform" || items[2].Name != "Website Redesign" {
			t.Fatalf("name asc: %+v, %v", items, err)
		}
	})

	t.Run("cross-tenant access is denied without leaking data", func(t *testing.T) {
		_, err := svc.Get(ctx, userB, orgB, p1.ID)
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		_, err = svc.Get(ctx, userA, orgB, p1.ID)
		requireAPIError(t, err, 404, "ORGANIZATION_NOT_FOUND")
		_, err = svc.Update(ctx, userB, orgB, p1.ID, decodeUpdate(t, `{"name":"Hacked"}`))
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		requireAPIError(t, svc.Delete(ctx, userB, orgB, p1.ID), 404, "PROJECT_NOT_FOUND")
		_, _, err = svc.ListActivity(ctx, userB, orgB, p1.ID, firstPage)
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		_, pag, err := svc.List(ctx, userB, orgB, projects.ListFilter{}, firstPage)
		if err != nil || pag.Total != 0 {
			t.Fatalf("org B sees org A projects: %+v, %v", pag, err)
		}
		_, err = svc.Update(ctx, userA, orgA, p1.ID, decodeUpdate(t, `{"team_id":"`+teamB.ID.String()+`"}`))
		requireAPIError(t, err, 422, "INVALID_TEAM")
	})

	t.Run("partial updates and validation", func(t *testing.T) {
		updated, err := svc.Update(ctx, userA, orgA, p1.ID, decodeUpdate(t, `{"status":"on_hold","team_id":null}`))
		if err != nil || updated.Status != "on_hold" || updated.TeamID != nil || updated.Priority != "high" {
			t.Fatalf("update: %+v, %v", updated, err)
		}
		_, err = svc.Update(ctx, userA, orgA, p1.ID, decodeUpdate(t, `{"start_date":"2026-10-10","due_date":"2026-10-01"}`))
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
		_, err = svc.Update(ctx, userA, orgA, uuid.New(), decodeUpdate(t, `{"name":"Ghost"}`))
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
	})

	t.Run("role permissions are enforced", func(t *testing.T) {
		userC, _ := registerOrg(t, pool, "proj-c@example.com", "Projects Home C")
		joinOrg(t, pool, orgA, userC, "Viewer")
		if _, pag, err := svc.List(ctx, userC, orgA, projects.ListFilter{}, firstPage); err != nil || pag.Total != 3 {
			t.Fatalf("viewer list: %+v, %v", pag, err)
		}
		_, err := svc.Create(ctx, userC, orgA, projects.CreateInput{Name: "Viewer Project"})
		requireAPIError(t, err, 403, "FORBIDDEN")
		_, err = svc.Update(ctx, userC, orgA, p3.ID, decodeUpdate(t, `{"name":"Renamed"}`))
		requireAPIError(t, err, 403, "FORBIDDEN")

		setRole(t, pool, orgA, userC, "Manager")
		managed, err := svc.Create(ctx, userC, orgA, projects.CreateInput{Name: "Manager Project"})
		if err != nil {
			t.Fatalf("manager create: %v", err)
		}
		requireAPIError(t, svc.Delete(ctx, userC, orgA, managed.ID), 403, "FORBIDDEN")
		if err := svc.Delete(ctx, userA, orgA, managed.ID); err != nil {
			t.Fatalf("owner delete: %v", err)
		}
	})

	t.Run("deleting a team unassigns its projects", func(t *testing.T) {
		p4, err := svc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Team Bound", TeamID: &teamA.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := teamSvc.Delete(ctx, userA, orgA, teamA.ID); err != nil {
			t.Fatalf("delete team: %v", err)
		}
		got, err := svc.Get(ctx, userA, orgA, p4.ID)
		if err != nil || got.TeamID != nil {
			t.Fatalf("project after team delete: %+v, %v", got, err)
		}
	})

	t.Run("activity is recorded transactionally", func(t *testing.T) {
		entries, pag, err := svc.ListActivity(ctx, userA, orgA, p1.ID, firstPage)
		if err != nil || pag.Total != 2 {
			t.Fatalf("activity: %+v, %+v, %v", entries, pag, err)
		}
		if entries[0].Action != "project.updated" || entries[1].Action != "project.created" {
			t.Fatalf("unexpected activity order: %s, %s", entries[0].Action, entries[1].Action)
		}
		var meta struct {
			Status struct{ From, To string } `json:"status"`
		}
		if err := json.Unmarshal(entries[0].Metadata, &meta); err != nil || meta.Status.From != "active" || meta.Status.To != "on_hold" {
			t.Fatalf("status metadata: %s, %v", entries[0].Metadata, err)
		}
		if *entries[0].ActorUserID != userA {
			t.Fatal("activity actor mismatch")
		}

		// Failed updates must not leave activity behind.
		_, _ = svc.Update(ctx, userA, orgA, p1.ID, decodeUpdate(t, `{"status":"bogus"}`))
		if _, pag, _ := svc.ListActivity(ctx, userA, orgA, p1.ID, firstPage); pag.Total != 2 {
			t.Fatalf("rejected update recorded activity: %+v", pag)
		}

		if err := svc.Delete(ctx, userA, orgA, p2.ID); err != nil {
			t.Fatalf("delete p2: %v", err)
		}
		_, err = svc.Get(ctx, userA, orgA, p2.ID)
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		var deleted int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE resource_id = $1 AND action = 'project.deleted'`, p2.ID).Scan(&deleted); err != nil || deleted != 1 {
			t.Fatalf("deleted activity rows = %d, %v", deleted, err)
		}
	})

	t.Run("database enforces tenant-consistent team references", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO projects (organization_id, team_id, name, created_by) VALUES ($1, $2, 'Direct', $3)`,
			orgA, teamB.ID, userA)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("expected foreign key violation, got %v", err)
		}
	})

	t.Run("RLS and append-only activity as the application role", func(t *testing.T) {
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
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE id = $1`, p1.ID).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("RLS leaked org A project into org B context: %d, %v", visible, err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE organization_id = $1`, orgA).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("RLS leaked org A activity: %d, %v", visible, err)
		}
		_ = tx.Rollback(ctx)

		_, err = pool.Exec(ctx, `UPDATE activity_logs SET action = 'tampered'`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected activity updates to be denied, got %v", err)
		}
		_, err = pool.Exec(ctx, `DELETE FROM activity_logs`)
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected activity deletes to be denied, got %v", err)
		}
	})
}
