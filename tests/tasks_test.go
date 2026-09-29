package organizations_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/fieldtypes"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/tasks"
)

func decodeTaskUpdate(t *testing.T, body string) tasks.UpdateInput {
	t.Helper()
	var in tasks.UpdateInput
	if err := json.Unmarshal([]byte(body), &in); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return in
}

func taskDate(t *testing.T, value string) *tasks.Date {
	t.Helper()
	parsed, err := time.Parse(fieldtypes.DateLayout, value)
	if err != nil {
		t.Fatal(err)
	}
	return &tasks.Date{Time: parsed}
}

func mustCreateTask(t *testing.T, svc *tasks.Service, ctx context.Context,
	userID, orgID, projectID uuid.UUID, in tasks.CreateInput,
) tasks.TaskDTO {
	t.Helper()
	task, err := svc.Create(ctx, userID, orgID, projectID, in)
	if err != nil {
		t.Fatalf("create task %q: %v", in.Title, err)
	}
	return task
}

// joinMember registers a user, adds an active membership to orgID, and returns
// the new member's user ID.
func joinMember(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, email, roleName string) uuid.UUID {
	t.Helper()
	userID, _ := registerOrg(t, pool, email, "Task Home "+email)
	joinOrg(t, pool, orgID, userID, roleName)
	return userID
}

func removeMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM organization_memberships WHERE organization_id = $1 AND user_id = $2`, orgID, userID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}
}

func titles(items []tasks.TaskDTO) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Title)
	}
	return out
}

func TestTasksTenantIsolationRBACAndActivity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	projectSvc := projects.NewService(pool, orgSvc)
	svc := tasks.NewService(pool, orgSvc)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userA, orgA := registerOrg(t, pool, "task-a@example.com", "Tasks Org A")
	userB, orgB := registerOrg(t, pool, "task-b@example.com", "Tasks Org B")

	// A freshly registered owner must already hold the task permissions.
	projectA, err := projectSvc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Website Redesign"})
	if err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB, err := projectSvc.Create(ctx, userB, orgB, projects.CreateInput{Name: "Design System"})
	if err != nil {
		t.Fatalf("create project B: %v", err)
	}
	otherProject, err := projectSvc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Mobile App"})
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}

	member := joinMember(t, pool, orgA, "task-member@example.com", "Member")

	t.Run("creation validates project, assignee, and defaults", func(t *testing.T) {
		// A project from another tenant is rejected without leaking it.
		_, err := svc.Create(ctx, userA, orgA, projectB.ID, tasks.CreateInput{Title: "Leak"})
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		// A user without an active membership cannot be assigned work.
		_, err = svc.Create(ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "Cross tenant", AssigneeID: &userB})
		requireAPIError(t, err, 422, "INVALID_ASSIGNEE")
		_, err = svc.Create(ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "   "})
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
		_, err = svc.Create(ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "X", Status: "finished"})
		requireAPIError(t, err, 400, "VALIDATION_ERROR")

		created, err := svc.Create(ctx, userA, orgA, projectA.ID, tasks.CreateInput{
			Title: "Design the header", AssigneeID: &member, DueDate: taskDate(t, "2026-12-01"),
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		if created.Status != "todo" || created.Priority != "medium" || created.ProjectID != projectA.ID ||
			created.AssigneeID == nil || *created.AssigneeID != member || created.CreatedBy != userA {
			t.Fatalf("unexpected task: %+v", created)
		}
		if created.DueDate == nil || created.DueDate.Format(fieldtypes.DateLayout) != "2026-12-01" {
			t.Fatalf("unexpected due date: %+v", created.DueDate)
		}
	})

	t1 := mustCreateTask(t, svc, ctx, userA, orgA, projectA.ID, tasks.CreateInput{
		Title: "Write copy", Status: "in_progress", Priority: "high",
	})
	t2 := mustCreateTask(t, svc, ctx, userA, orgA, projectA.ID, tasks.CreateInput{
		Title: "Ship dark mode", Priority: "urgent", DueDate: taskDate(t, "2026-10-01"),
	})
	t3 := mustCreateTask(t, svc, ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "Archive backlog"})
	mustCreateTask(t, svc, ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "Retire legacy endpoint"})
	if _, err := svc.Create(ctx, userA, orgA, otherProject.ID, tasks.CreateInput{Title: "Mobile task"}); err != nil {
		t.Fatalf("create mobile task: %v", err)
	}

	t.Run("filtering, sorting, and pagination", func(t *testing.T) {
		inProgress := "in_progress"
		items, pag, err := svc.List(ctx, userA, orgA, tasks.ListFilter{Status: &inProgress}, firstPage)
		if err != nil || pag.Total != 1 || items[0].ID != t1.ID {
			t.Fatalf("status filter: %+v, %+v, %v", items, pag, err)
		}
		items, pag, err = svc.List(ctx, userA, orgA, tasks.ListFilter{ProjectID: &otherProject.ID}, firstPage)
		if err != nil || pag.Total != 1 || items[0].Title != "Mobile task" {
			t.Fatalf("project filter: %+v, %+v, %v", items, pag, err)
		}
		items, pag, err = svc.List(ctx, userA, orgA, tasks.ListFilter{AssigneeID: &member}, firstPage)
		if err != nil || pag.Total != 1 || items[0].Title != "Design the header" {
			t.Fatalf("assignee filter: %+v, %+v, %v", items, pag, err)
		}
		items, pag, err = svc.List(ctx, userA, orgA, tasks.ListFilter{Unassigned: true, ProjectID: &projectA.ID}, firstPage)
		if err != nil || pag.Total != 4 {
			t.Fatalf("unassigned filter: %d, %+v, %v", pag.Total, items, err)
		}
		// Ascending status follows the workflow order, so "todo" comes first.
		items, pag, err = svc.List(ctx, userA, orgA, tasks.ListFilter{Sort: "status"}, firstPage)
		if err != nil || pag.Total != 6 || items[0].Status != "todo" || items[5].Status != "in_progress" {
			t.Fatalf("status sort: %+v, %+v, %v", items, pag, err)
		}
		items, _, err = svc.List(ctx, userA, orgA,
			tasks.ListFilter{Sort: "title", ProjectID: &projectA.ID}, firstPage)
		if err != nil || !slices.Equal(titles(items), []string{
			"Archive backlog", "Design the header", "Retire legacy endpoint", "Ship dark mode", "Write copy",
		}) {
			t.Fatalf("title asc: %+v, %v", titles(items), err)
		}
		byPriority := tasks.ListFilter{Sort: "priority", Desc: true, ProjectID: &projectA.ID}
		items, pag, err = svc.List(ctx, userA, orgA, byPriority, httpx.PageRequest{Page: 1, PageSize: 3})
		if err != nil || pag.Total != 5 || pag.TotalPages != 2 || len(items) != 3 ||
			items[0].ID != t2.ID || items[1].ID != t1.ID {
			t.Fatalf("priority page 1: %+v, %+v, %v", items, pag, err)
		}
		items, _, err = svc.List(ctx, userA, orgA, byPriority, httpx.PageRequest{Page: 2, PageSize: 3})
		if err != nil || len(items) != 2 {
			t.Fatalf("priority page 2: %+v, %v", items, err)
		}
		// Per-project listing ignores any client-supplied project_id.
		items, pag, err = svc.ListByProject(ctx, userA, orgA, projectA.ID, tasks.ListFilter{}, firstPage)
		if err != nil || pag.Total != 5 || len(items) != 5 {
			t.Fatalf("list by project: %+v, %v", pag, err)
		}
		_, _, err = svc.ListByProject(ctx, userA, orgA, projectB.ID, tasks.ListFilter{}, firstPage)
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
	})

	t.Run("cross-tenant access is denied without leaking data", func(t *testing.T) {
		_, err := svc.Get(ctx, userB, orgB, t1.ID)
		requireAPIError(t, err, 404, "TASK_NOT_FOUND")
		_, err = svc.Get(ctx, userA, orgB, t1.ID)
		requireAPIError(t, err, 404, "ORGANIZATION_NOT_FOUND")
		_, err = svc.Update(ctx, userB, orgB, t1.ID, decodeTaskUpdate(t, `{"title":"Hacked"}`))
		requireAPIError(t, err, 404, "TASK_NOT_FOUND")
		requireAPIError(t, svc.Delete(ctx, userB, orgB, t1.ID), 404, "TASK_NOT_FOUND")
		_, _, err = svc.ListActivity(ctx, userB, orgB, t1.ID, firstPage)
		requireAPIError(t, err, 404, "TASK_NOT_FOUND")
		_, pag, err := svc.List(ctx, userB, orgB, tasks.ListFilter{}, firstPage)
		if err != nil || pag.Total != 0 {
			t.Fatalf("org B sees org A tasks: %+v, %v", pag, err)
		}
		// Org B cannot create a task in org B pointing at org A's project.
		_, err = svc.Create(ctx, userB, orgB, projectA.ID, tasks.CreateInput{Title: "Injected"})
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
	})

	t.Run("partial updates, assignment, and validation", func(t *testing.T) {
		assignee := joinMember(t, pool, orgA, "task-assignee@example.com", "Member")
		updated, err := svc.Update(ctx, userA, orgA, t1.ID, decodeTaskUpdate(t,
			`{"status":"blocked","assignee_id":"`+assignee.String()+`","due_date":"2026-11-30"}`))
		if err != nil || updated.Status != "blocked" || updated.AssigneeID == nil ||
			*updated.AssigneeID != assignee || updated.Title != "Write copy" ||
			updated.Priority != "high" {
			t.Fatalf("update: %+v, %v", updated, err)
		}
		// Explicit null unassigns the task.
		updated, err = svc.Update(ctx, userA, orgA, t1.ID, decodeTaskUpdate(t, `{"assignee_id":null}`))
		if err != nil || updated.AssigneeID != nil {
			t.Fatalf("unassign: %+v, %v", updated, err)
		}
		// A user who is not an active member is never assignable.
		_, err = svc.Update(ctx, userA, orgA, t1.ID, decodeTaskUpdate(t, `{"assignee_id":"`+userB.String()+`"}`))
		requireAPIError(t, err, 422, "INVALID_ASSIGNEE")
		_, err = svc.Update(ctx, userA, orgA, t1.ID, decodeTaskUpdate(t, `{"status":"finished"}`))
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
		_, err = svc.Update(ctx, userA, orgA, uuid.New(), decodeTaskUpdate(t, `{"title":"Ghost"}`))
		requireAPIError(t, err, 404, "TASK_NOT_FOUND")

		// A no-op update leaves the row untouched and records no activity.
		before, _, err := svc.ListActivity(ctx, userA, orgA, t1.ID, firstPage)
		if err != nil {
			t.Fatalf("activity before no-op: %v", err)
		}
		unchanged, err := svc.Update(ctx, userA, orgA, t1.ID,
			decodeTaskUpdate(t, `{"title":"Write copy","status":"blocked"}`))
		if err != nil {
			t.Fatalf("no-op update: %v", err)
		}
		if unchanged.Status != "blocked" {
			t.Fatalf("no-op update changed the task: %+v", unchanged)
		}
		if after, _, _ := svc.ListActivity(ctx, userA, orgA, t1.ID, firstPage); len(after) != len(before) {
			t.Fatalf("no-op update recorded activity: %d -> %d", len(before), len(after))
		}
	})

	t.Run("role permissions are enforced", func(t *testing.T) {
		viewer := joinMember(t, pool, orgA, "task-viewer@example.com", "Viewer")
		if _, pag, err := svc.List(ctx, viewer, orgA, tasks.ListFilter{}, firstPage); err != nil || pag.Total == 0 {
			t.Fatalf("viewer list: %+v, %v", pag, err)
		}
		if _, _, err := svc.ListActivity(ctx, viewer, orgA, t1.ID, firstPage); err != nil {
			t.Fatalf("viewer activity: %v", err)
		}
		_, err := svc.Create(ctx, viewer, orgA, projectA.ID, tasks.CreateInput{Title: "Viewer task"})
		requireAPIError(t, err, 403, "FORBIDDEN")
		_, err = svc.Update(ctx, viewer, orgA, t1.ID, decodeTaskUpdate(t, `{"title":"Renamed"}`))
		requireAPIError(t, err, 403, "FORBIDDEN")
		requireAPIError(t, svc.Delete(ctx, viewer, orgA, t1.ID), 403, "FORBIDDEN")

		manager := joinMember(t, pool, orgA, "task-manager@example.com", "Manager")
		managed, err := svc.Create(ctx, manager, orgA, projectA.ID, tasks.CreateInput{Title: "Manager task"})
		if err != nil {
			t.Fatalf("manager create: %v", err)
		}
		if _, err := svc.Update(ctx, manager, orgA, managed.ID, decodeTaskUpdate(t, `{"status":"in_review"}`)); err != nil {
			t.Fatalf("manager update: %v", err)
		}
		// Deleting a task stays with Owner and Admin.
		requireAPIError(t, svc.Delete(ctx, manager, orgA, managed.ID), 403, "FORBIDDEN")
		if err := svc.Delete(ctx, userA, orgA, managed.ID); err != nil {
			t.Fatalf("owner delete: %v", err)
		}
	})

	t.Run("activity is recorded transactionally", func(t *testing.T) {
		entries, pag, err := svc.ListActivity(ctx, userA, orgA, t1.ID, firstPage)
		if err != nil || pag.Total != 3 {
			t.Fatalf("activity: %+v, %+v, %v", entries, pag, err)
		}
		if entries[0].Action != "task.updated" || entries[2].Action != "task.created" {
			t.Fatalf("unexpected activity order: %s, %s", entries[0].Action, entries[2].Action)
		}
		// The newest entry is the unassignment, the one before it the combined
		// status change and assignment.
		var unassigned, assigned struct {
			Changes  []string                  `json:"changes"`
			Status   struct{ From, To string } `json:"status"`
			Assignee struct {
				From *uuid.UUID `json:"from"`
				To   *uuid.UUID `json:"to"`
			} `json:"assignee"`
		}
		if err := json.Unmarshal(entries[0].Metadata, &unassigned); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(unassigned.Changes, []string{"assignee_id"}) ||
			unassigned.Assignee.From == nil || unassigned.Assignee.To != nil {
			t.Fatalf("unassign metadata: %s", entries[0].Metadata)
		}
		if err := json.Unmarshal(entries[1].Metadata, &assigned); err != nil {
			t.Fatal(err)
		}
		if assigned.Status.From != "in_progress" || assigned.Status.To != "blocked" ||
			assigned.Assignee.From != nil || assigned.Assignee.To == nil {
			t.Fatalf("assign metadata: %s", entries[1].Metadata)
		}
		if *entries[0].ActorUserID != userA {
			t.Fatal("activity actor mismatch")
		}
		// Task activity is tenant-scoped: it never mixes resources.
		entries, _, err = svc.ListActivity(ctx, userA, orgA, t2.ID, firstPage)
		if err != nil || len(entries) != 1 || entries[0].Action != "task.created" {
			t.Fatalf("unexpected activity for t2: %+v, %v", entries, err)
		}

		// Rejected updates must not leave activity behind.
		_, _ = svc.Update(ctx, userA, orgA, t1.ID, decodeTaskUpdate(t, `{"status":"bogus"}`))
		if _, pag, _ := svc.ListActivity(ctx, userA, orgA, t1.ID, firstPage); pag.Total != 3 {
			t.Fatalf("rejected update recorded activity: %+v", pag)
		}

		if err := svc.Delete(ctx, userA, orgA, t3.ID); err != nil {
			t.Fatalf("delete t3: %v", err)
		}
		_, err = svc.Get(ctx, userA, orgA, t3.ID)
		requireAPIError(t, err, 404, "TASK_NOT_FOUND")
		var deleted int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM activity_logs WHERE resource_id = $1 AND action = 'task.deleted'`, t3.ID).Scan(&deleted); err != nil || deleted != 1 {
			t.Fatalf("deleted activity rows = %d, %v", deleted, err)
		}
	})

	t.Run("removing a member unassigns their tasks", func(t *testing.T) {
		leaving := joinMember(t, pool, orgA, "task-leaving@example.com", "Member")
		assigned, err := svc.Create(ctx, userA, orgA, projectA.ID, tasks.CreateInput{Title: "Owned work", AssigneeID: &leaving})
		if err != nil {
			t.Fatalf("create assigned task: %v", err)
		}
		removeMembership(t, pool, orgA, leaving)
		got, err := svc.Get(ctx, userA, orgA, assigned.ID)
		if err != nil {
			t.Fatalf("get after membership removal: %v", err)
		}
		if got.AssigneeID != nil {
			t.Fatalf("expected the task to be unassigned, got %v", *got.AssigneeID)
		}
	})

	t.Run("deleting a project removes its tasks", func(t *testing.T) {
		doomed, err := projectSvc.Create(ctx, userA, orgA, projects.CreateInput{Name: "Doomed"})
		if err != nil {
			t.Fatalf("create doomed project: %v", err)
		}
		if _, err := svc.Create(ctx, userA, orgA, doomed.ID, tasks.CreateInput{Title: "Doomed task"}); err != nil {
			t.Fatalf("create doomed task: %v", err)
		}
		if err := projectSvc.Delete(ctx, userA, orgA, doomed.ID); err != nil {
			t.Fatalf("delete project: %v", err)
		}
		// Filtering by the deleted project is a 404, and the task itself is gone.
		_, _, err = svc.List(ctx, userA, orgA, tasks.ListFilter{ProjectID: &doomed.ID}, firstPage)
		requireAPIError(t, err, 404, "PROJECT_NOT_FOUND")
		var remaining int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE title = 'Doomed task'`).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("tasks survived project deletion: %d, %v", remaining, err)
		}
	})

	t.Run("database enforces tenant-consistent references", func(t *testing.T) {
		var pgErr *pgconn.PgError
		_, err := pool.Exec(ctx, `
			INSERT INTO tasks (organization_id, project_id, title, created_by)
			VALUES ($1, $2, 'Direct project', $3)`, orgA, projectB.ID, userA)
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "tasks_project_fkey" {
			t.Fatalf("expected project foreign key violation, got %v", err)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO tasks (organization_id, project_id, title, assignee_id, created_by)
			VALUES ($1, $2, 'Direct assignee', $3, $4)`, orgA, projectA.ID, userB, userA)
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "tasks_assignee_fkey" {
			t.Fatalf("expected assignee foreign key violation, got %v", err)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO tasks (organization_id, project_id, title, status, created_by)
			VALUES ($1, $2, 'Bad status', 'finished', $3)`, orgA, projectA.ID, userA)
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check violation, got %v", err)
		}
	})

	t.Run("RLS isolates tasks", func(t *testing.T) {
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
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE id = $1`, t1.ID).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("RLS leaked org A task into org B context: %d, %v", visible, err)
		}
	})
}
