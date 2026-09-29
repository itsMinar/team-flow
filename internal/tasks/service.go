package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/activity"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/fieldtypes"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/permissions"
)

const (
	projectForeignKey  = "tasks_project_fkey"
	assigneeForeignKey = "tasks_assignee_fkey"
)

// Service owns task use cases. Authorization is always resolved from current
// organization state, never from token claims.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	orgs *organizations.Service
}

func NewService(pool *pgxpool.Pool, orgs *organizations.Service) *Service {
	return &Service{pool: pool, q: db.New(pool), orgs: orgs}
}

func taskNotFound(err error) error {
	return httpx.NewAPIError(404, "TASK_NOT_FOUND", "Task not found", err)
}

func projectNotFound(err error) error {
	return httpx.NewAPIError(404, "PROJECT_NOT_FOUND", "Project not found in this organization", err)
}

func invalidAssignee(err error) error {
	return httpx.NewAPIError(422, "INVALID_ASSIGNEE", "Assignee must be an active member of this organization", err)
}

// ensureProject rejects project IDs from other tenants; the composite foreign
// key backstops concurrent deletes.
func ensureProject(ctx context.Context, q *db.Queries, orgID, projectID uuid.UUID) error {
	_, err := q.GetProject(ctx, db.GetProjectParams{ID: projectID, OrganizationID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return projectNotFound(err)
	}
	return err
}

// ensureAssignee rejects users who are not active members of the organization,
// so a task can never be assigned across a tenant boundary.
func ensureAssignee(ctx context.Context, q *db.Queries, orgID uuid.UUID, assigneeID *uuid.UUID) error {
	if assigneeID == nil {
		return nil
	}
	membership, err := q.GetMembership(ctx, db.GetMembershipParams{OrganizationID: orgID, UserID: *assigneeID})
	if errors.Is(err, pgx.ErrNoRows) || membership.Status != "active" {
		return invalidAssignee(err)
	}
	return err
}

// mapWriteError converts database constraint violations into the same safe
// domain errors the service returns for the equivalent pre-checks.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch {
	case pgErr.Code == "23503" && pgErr.ConstraintName == projectForeignKey:
		return projectNotFound(err)
	case pgErr.Code == "23503" && pgErr.ConstraintName == assigneeForeignKey:
		return invalidAssignee(err)
	}
	return err
}

func (s *Service) getForUpdate(ctx context.Context, q *db.Queries, orgID, taskID uuid.UUID) (db.Task, error) {
	t, err := q.GetTaskForUpdate(ctx, db.GetTaskForUpdateParams{ID: taskID, OrganizationID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Task{}, taskNotFound(err)
	}
	return t, err
}

func (s *Service) list(ctx context.Context, userID, orgID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]TaskDTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksRead); err != nil {
		return nil, httpx.Pagination{}, err
	}
	sortBy := filter.Sort
	if !sortableFields[sortBy] {
		sortBy = defaultSort
	}
	var rows []db.Task
	var total int64
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if filter.ProjectID != nil {
			if err := ensureProject(ctx, q, orgID, *filter.ProjectID); err != nil {
				return err
			}
		}
		countParams := db.CountTasksParams{
			OrganizationID: orgID, ProjectID: filter.ProjectID, Status: filter.Status,
			Priority: filter.Priority, AssigneeID: filter.AssigneeID, Unassigned: filter.Unassigned,
		}
		var err error
		if total, err = q.CountTasks(ctx, countParams); err != nil {
			return err
		}
		rows, err = q.ListTasks(ctx, db.ListTasksParams{
			OrganizationID: orgID, ProjectID: filter.ProjectID, Status: filter.Status,
			Priority: filter.Priority, AssigneeID: filter.AssigneeID, Unassigned: filter.Unassigned,
			SortBy: sortBy, SortDesc: filter.Desc, PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list tasks: %w", err)
	}
	result := make([]TaskDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, toDTO(row))
	}
	return result, httpx.NewPagination(page, total), nil
}

// List returns every task in the organization, optionally narrowed to one
// project.
func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]TaskDTO, httpx.Pagination, error) {
	return s.list(ctx, userID, orgID, filter, page)
}

// ListByProject returns the tasks of a single project in the organization.
func (s *Service) ListByProject(ctx context.Context, userID, orgID, projectID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]TaskDTO, httpx.Pagination, error) {
	scoped := filter
	scoped.ProjectID = &projectID
	return s.list(ctx, userID, orgID, scoped, page)
}

func (s *Service) Get(ctx context.Context, userID, orgID, taskID uuid.UUID) (TaskDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksRead); err != nil {
		return TaskDTO{}, err
	}
	var task db.Task
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		var err error
		task, err = s.q.WithTx(tx).GetTask(ctx, db.GetTaskParams{ID: taskID, OrganizationID: orgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return taskNotFound(err)
		}
		return err
	})
	if err != nil {
		return TaskDTO{}, fmt.Errorf("get task: %w", err)
	}
	return toDTO(task), nil
}

// Create adds a task to a project of the caller's organization.
func (s *Service) Create(ctx context.Context, userID, orgID, projectID uuid.UUID, in CreateInput) (TaskDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksCreate); err != nil {
		return TaskDTO{}, err
	}
	f := in.fields()
	if err := f.validate(); err != nil {
		return TaskDTO{}, err
	}
	var task db.Task
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := ensureProject(ctx, q, orgID, projectID); err != nil {
			return err
		}
		if err := ensureAssignee(ctx, q, orgID, f.AssigneeID); err != nil {
			return err
		}
		var err error
		task, err = q.CreateTask(ctx, db.CreateTaskParams{
			OrganizationID: orgID, ProjectID: projectID, Title: f.Title, Description: f.Description,
			Status: f.Status, Priority: f.Priority, AssigneeID: f.AssigneeID,
			DueDate: fieldtypes.TimeFromDate(f.DueDate), CreatedBy: userID,
		})
		if err != nil {
			return mapWriteError(err)
		}
		metadata := map[string]any{"title": task.Title, "project_id": projectID}
		if task.AssigneeID != nil {
			metadata["assignee_id"] = task.AssigneeID
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.TaskCreated,
			ResourceType: activity.ResourceTask, ResourceID: task.ID, Metadata: metadata,
		})
	})
	if err != nil {
		return TaskDTO{}, fmt.Errorf("create task: %w", err)
	}
	return toDTO(task), nil
}

// Update applies a partial update, including reassignment and status changes.
func (s *Service) Update(ctx context.Context, userID, orgID, taskID uuid.UUID, in UpdateInput) (TaskDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksUpdate); err != nil {
		return TaskDTO{}, err
	}
	var task db.Task
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := s.getForUpdate(ctx, q, orgID, taskID)
		if err != nil {
			return err
		}
		next, changed := in.apply(fieldsFromTask(current))
		if err := next.validate(); err != nil {
			return err
		}
		if len(changed) == 0 {
			task = current
			return nil
		}
		if in.AssigneeID.Set {
			if err := ensureAssignee(ctx, q, orgID, next.AssigneeID); err != nil {
				return err
			}
		}
		task, err = q.UpdateTask(ctx, db.UpdateTaskParams{
			ID: taskID, OrganizationID: orgID, Title: next.Title, Description: next.Description,
			Status: next.Status, Priority: next.Priority, AssigneeID: next.AssigneeID,
			DueDate: fieldtypes.TimeFromDate(next.DueDate),
		})
		if err != nil {
			return mapWriteError(err)
		}
		metadata := map[string]any{"title": task.Title, "changes": changed}
		if current.Status != task.Status {
			metadata["status"] = map[string]string{"from": current.Status, "to": task.Status}
		}
		if !fieldtypes.EqualPtr(current.AssigneeID, task.AssigneeID, func(a, b uuid.UUID) bool { return a == b }) {
			metadata["assignee"] = map[string]any{"from": current.AssigneeID, "to": task.AssigneeID}
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.TaskUpdated,
			ResourceType: activity.ResourceTask, ResourceID: task.ID, Metadata: metadata,
		})
	})
	if err != nil {
		return TaskDTO{}, fmt.Errorf("update task: %w", err)
	}
	return toDTO(task), nil
}

func (s *Service) Delete(ctx context.Context, userID, orgID, taskID uuid.UUID) error {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksDelete); err != nil {
		return err
	}
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := s.getForUpdate(ctx, q, orgID, taskID)
		if err != nil {
			return err
		}
		if err := q.DeleteTask(ctx, db.DeleteTaskParams{ID: taskID, OrganizationID: orgID}); err != nil {
			return err
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.TaskDeleted,
			ResourceType: activity.ResourceTask, ResourceID: taskID,
			Metadata: map[string]any{"title": current.Title, "project_id": current.ProjectID},
		})
	})
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}

// ListActivity returns the append-only activity trail of a single task.
func (s *Service) ListActivity(ctx context.Context, userID, orgID, taskID uuid.UUID, page httpx.PageRequest) ([]activity.DTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.TasksRead); err != nil {
		return nil, httpx.Pagination{}, err
	}
	var rows []db.ActivityLog
	var total int64
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetTask(ctx, db.GetTaskParams{ID: taskID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return taskNotFound(err)
		} else if err != nil {
			return err
		}
		var err error
		total, err = q.CountActivityByResource(ctx, db.CountActivityByResourceParams{
			OrganizationID: orgID, ResourceType: activity.ResourceTask, ResourceID: taskID,
		})
		if err != nil {
			return err
		}
		rows, err = q.ListActivityByResource(ctx, db.ListActivityByResourceParams{
			OrganizationID: orgID, ResourceType: activity.ResourceTask, ResourceID: taskID,
			PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list task activity: %w", err)
	}
	result := make([]activity.DTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, activity.ToDTO(row))
	}
	return result, httpx.NewPagination(page, total), nil
}
