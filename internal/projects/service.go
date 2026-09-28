// Package projects implements organization-scoped projects with filtering,
// sorting, pagination, and activity logging.
package projects

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
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/permissions"
)

const teamForeignKey = "projects_team_fkey"

type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	orgs *organizations.Service
}

func NewService(pool *pgxpool.Pool, orgs *organizations.Service) *Service {
	return &Service{pool: pool, q: db.New(pool), orgs: orgs}
}

func projectNotFound(err error) error {
	return httpx.NewAPIError(404, "PROJECT_NOT_FOUND", "Project not found", err)
}

func invalidTeam(err error) error {
	return httpx.NewAPIError(422, "INVALID_TEAM", "Team not found in this organization", err)
}

// ensureTeam rejects team IDs from other tenants; the composite foreign key backstops concurrent deletes.
func ensureTeam(ctx context.Context, q *db.Queries, orgID uuid.UUID, teamID *uuid.UUID) error {
	if teamID == nil {
		return nil
	}
	_, err := q.GetTeam(ctx, db.GetTeamParams{ID: *teamID, OrganizationID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return invalidTeam(err)
	}
	return err
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == teamForeignKey {
		return invalidTeam(err)
	}
	return err
}

func (s *Service) getForUpdate(ctx context.Context, q *db.Queries, orgID, projectID uuid.UUID) (db.Project, error) {
	p, err := q.GetProjectForUpdate(ctx, db.GetProjectForUpdateParams{ID: projectID, OrganizationID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Project{}, projectNotFound(err)
	}
	return p, err
}

func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]ProjectDTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsRead); err != nil {
		return nil, httpx.Pagination{}, err
	}
	sortBy := filter.Sort
	if !sortableFields[sortBy] {
		sortBy = defaultSort
	}
	var rows []db.Project
	var total int64
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		total, err = q.CountProjects(ctx, db.CountProjectsParams{
			OrganizationID: orgID, Status: filter.Status, Priority: filter.Priority, TeamID: filter.TeamID,
		})
		if err != nil {
			return err
		}
		rows, err = q.ListProjects(ctx, db.ListProjectsParams{
			OrganizationID: orgID, Status: filter.Status, Priority: filter.Priority, TeamID: filter.TeamID,
			SortBy: sortBy, SortDesc: filter.Desc, PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list projects: %w", err)
	}
	result := make([]ProjectDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, toDTO(row))
	}
	return result, httpx.NewPagination(page, total), nil
}

func (s *Service) Get(ctx context.Context, userID, orgID, projectID uuid.UUID) (ProjectDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsRead); err != nil {
		return ProjectDTO{}, err
	}
	var project db.Project
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		var err error
		project, err = s.q.WithTx(tx).GetProject(ctx, db.GetProjectParams{ID: projectID, OrganizationID: orgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return projectNotFound(err)
		}
		return err
	})
	if err != nil {
		return ProjectDTO{}, fmt.Errorf("get project: %w", err)
	}
	return toDTO(project), nil
}

func (s *Service) Create(ctx context.Context, userID, orgID uuid.UUID, in CreateInput) (ProjectDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsCreate); err != nil {
		return ProjectDTO{}, err
	}
	f := in.fields()
	if err := f.validate(); err != nil {
		return ProjectDTO{}, err
	}
	var project db.Project
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := ensureTeam(ctx, q, orgID, f.TeamID); err != nil {
			return err
		}
		var err error
		project, err = q.CreateProject(ctx, db.CreateProjectParams{
			OrganizationID: orgID, TeamID: f.TeamID, Name: f.Name, Description: f.Description,
			Status: f.Status, Priority: f.Priority,
			StartDate: timeFromDate(f.StartDate), DueDate: timeFromDate(f.DueDate), CreatedBy: userID,
		})
		if err != nil {
			return mapWriteError(err)
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.ProjectCreated,
			ResourceType: activity.ResourceProject, ResourceID: project.ID,
			Metadata: map[string]any{"name": project.Name},
		})
	})
	if err != nil {
		return ProjectDTO{}, fmt.Errorf("create project: %w", err)
	}
	return toDTO(project), nil
}

func (s *Service) Update(ctx context.Context, userID, orgID, projectID uuid.UUID, in UpdateInput) (ProjectDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsUpdate); err != nil {
		return ProjectDTO{}, err
	}
	var project db.Project
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := s.getForUpdate(ctx, q, orgID, projectID)
		if err != nil {
			return err
		}
		next, changed := in.apply(fieldsFromProject(current))
		if err := next.validate(); err != nil {
			return err
		}
		if len(changed) == 0 {
			project = current
			return nil
		}
		if in.TeamID.Set {
			if err := ensureTeam(ctx, q, orgID, next.TeamID); err != nil {
				return err
			}
		}
		project, err = q.UpdateProject(ctx, db.UpdateProjectParams{
			ID: projectID, OrganizationID: orgID, TeamID: next.TeamID, Name: next.Name,
			Description: next.Description, Status: next.Status, Priority: next.Priority,
			StartDate: timeFromDate(next.StartDate), DueDate: timeFromDate(next.DueDate),
		})
		if err != nil {
			return mapWriteError(err)
		}
		metadata := map[string]any{"name": project.Name, "changes": changed}
		if current.Status != project.Status {
			metadata["status"] = map[string]string{"from": current.Status, "to": project.Status}
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.ProjectUpdated,
			ResourceType: activity.ResourceProject, ResourceID: project.ID, Metadata: metadata,
		})
	})
	if err != nil {
		return ProjectDTO{}, fmt.Errorf("update project: %w", err)
	}
	return toDTO(project), nil
}

func (s *Service) Delete(ctx context.Context, userID, orgID, projectID uuid.UUID) error {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsDelete); err != nil {
		return err
	}
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := s.getForUpdate(ctx, q, orgID, projectID)
		if err != nil {
			return err
		}
		if err := q.DeleteProject(ctx, db.DeleteProjectParams{ID: projectID, OrganizationID: orgID}); err != nil {
			return err
		}
		return activity.Record(ctx, q, activity.Entry{
			OrganizationID: orgID, ActorUserID: &userID, Action: activity.ProjectDeleted,
			ResourceType: activity.ResourceProject, ResourceID: projectID,
			Metadata: map[string]any{"name": current.Name},
		})
	})
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}

func (s *Service) ListActivity(ctx context.Context, userID, orgID, projectID uuid.UUID, page httpx.PageRequest) ([]activity.DTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.ProjectsRead); err != nil {
		return nil, httpx.Pagination{}, err
	}
	var rows []db.ActivityLog
	var total int64
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetProject(ctx, db.GetProjectParams{ID: projectID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return projectNotFound(err)
		} else if err != nil {
			return err
		}
		var err error
		total, err = q.CountActivityByResource(ctx, db.CountActivityByResourceParams{
			OrganizationID: orgID, ResourceType: activity.ResourceProject, ResourceID: projectID,
		})
		if err != nil {
			return err
		}
		rows, err = q.ListActivityByResource(ctx, db.ListActivityByResourceParams{
			OrganizationID: orgID, ResourceType: activity.ResourceProject, ResourceID: projectID,
			PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list project activity: %w", err)
	}
	result := make([]activity.DTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, activity.ToDTO(row))
	}
	return result, httpx.NewPagination(page, total), nil
}
