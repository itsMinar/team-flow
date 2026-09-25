// Package teams implements organization-scoped teams and team memberships.
package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
)

const (
	maxTeamNameLength        = 100
	maxTeamDescriptionLength = 500
)

type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	orgs   *organizations.Service
	logger *slog.Logger
}

type TeamDTO struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Name           string    `json:"name"`
	Description    *string   `json:"description,omitempty"`
	CreatedBy      uuid.UUID `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type MemberDTO struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	CreatedAt time.Time `json:"created_at"`
}

func NewService(pool *pgxpool.Pool, orgs *organizations.Service, logger *slog.Logger) *Service {
	return &Service{pool: pool, q: db.New(pool), orgs: orgs, logger: logger}
}

func (s *Service) withOrgTx(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", orgID.String()); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *Service) authorize(ctx context.Context, userID, orgID uuid.UUID, permission string) (authctx.Tenant, error) {
	tenant, err := s.orgs.ResolveTenant(ctx, userID, orgID)
	if err != nil {
		return authctx.Tenant{}, err
	}
	if err := s.orgs.RequirePermission(ctx, orgID, tenant.RoleID, permission); err != nil {
		return authctx.Tenant{}, err
	}
	return tenant, nil
}

func toDTO(team db.Team) TeamDTO {
	return TeamDTO{ID: team.ID, OrganizationID: team.OrganizationID, Name: team.Name,
		Description: team.Description, CreatedBy: team.CreatedBy, CreatedAt: team.CreatedAt,
		UpdatedAt: team.UpdatedAt}
}

func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID) ([]TeamDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsRead); err != nil {
		return nil, err
	}
	var rows []db.Team
	if err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		rows, err = s.q.WithTx(tx).ListTeams(ctx, orgID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	result := make([]TeamDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, toDTO(row))
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, userID, orgID, teamID uuid.UUID) (TeamDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsRead); err != nil {
		return TeamDTO{}, err
	}
	var team db.Team
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		team, err = s.q.WithTx(tx).GetTeam(ctx, db.GetTeamParams{ID: teamID, OrganizationID: orgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		return err
	})
	if err != nil {
		return TeamDTO{}, err
	}
	return toDTO(team), nil
}

func (s *Service) Create(ctx context.Context, userID, orgID uuid.UUID, name string, description *string) (TeamDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsManage); err != nil {
		return TeamDTO{}, err
	}
	var team db.Team
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		team, err = s.q.WithTx(tx).CreateTeam(ctx, db.CreateTeamParams{
			OrganizationID: orgID, Name: name, Description: description, CreatedBy: userID,
		})
		if isUniqueViolation(err) {
			return httpx.NewAPIError(409, "TEAM_NAME_TAKEN", "A team with this name already exists", err)
		}
		return err
	})
	if err != nil {
		return TeamDTO{}, fmt.Errorf("create team: %w", err)
	}
	return toDTO(team), nil
}

func (s *Service) Update(ctx context.Context, userID, orgID, teamID uuid.UUID, name string, description *string) (TeamDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsManage); err != nil {
		return TeamDTO{}, err
	}
	var team db.Team
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		team, err = s.q.WithTx(tx).UpdateTeam(ctx, db.UpdateTeamParams{
			ID: teamID, OrganizationID: orgID, Name: name, Description: description,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		}
		if isUniqueViolation(err) {
			return httpx.NewAPIError(409, "TEAM_NAME_TAKEN", "A team with this name already exists", err)
		}
		return err
	})
	if err != nil {
		return TeamDTO{}, fmt.Errorf("update team: %w", err)
	}
	return toDTO(team), nil
}

func (s *Service) Delete(ctx context.Context, userID, orgID, teamID uuid.UUID) error {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsManage); err != nil {
		return err
	}
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetTeam(ctx, db.GetTeamParams{ID: teamID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		return q.DeleteTeam(ctx, db.DeleteTeamParams{ID: teamID, OrganizationID: orgID})
	})
}

func (s *Service) ListMembers(ctx context.Context, userID, orgID, teamID uuid.UUID) ([]MemberDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsRead); err != nil {
		return nil, err
	}
	var rows []db.ListTeamMembersRow
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetTeam(ctx, db.GetTeamParams{ID: teamID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		var err error
		rows, err = q.ListTeamMembers(ctx, db.ListTeamMembersParams{TeamID: teamID, OrganizationID: orgID})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	result := make([]MemberDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, MemberDTO{ID: row.ID, UserID: row.UserID, Email: row.Email,
			FirstName: row.FirstName, LastName: row.LastName, CreatedAt: row.CreatedAt})
	}
	return result, nil
}

func (s *Service) AddMember(ctx context.Context, userID, orgID, teamID, memberUserID uuid.UUID) (MemberDTO, error) {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsManage); err != nil {
		return MemberDTO{}, err
	}
	var member MemberDTO
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetTeam(ctx, db.GetTeamParams{ID: teamID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		membership, err := q.GetMembership(ctx, db.GetMembershipParams{OrganizationID: orgID, UserID: memberUserID})
		if errors.Is(err, pgx.ErrNoRows) || membership.Status != "active" {
			return httpx.NewAPIError(404, "MEMBER_NOT_FOUND", "Active organization member not found", err)
		}
		row, err := q.AddTeamMember(ctx, db.AddTeamMemberParams{OrganizationID: orgID, TeamID: teamID, UserID: memberUserID})
		if isUniqueViolation(err) {
			return httpx.NewAPIError(409, "TEAM_MEMBER_EXISTS", "User is already on this team", err)
		}
		if err != nil {
			return err
		}
		user, err := q.GetUserByID(ctx, memberUserID)
		if err != nil {
			return err
		}
		member = MemberDTO{ID: row.ID, UserID: row.UserID, Email: user.Email, FirstName: user.FirstName, LastName: user.LastName, CreatedAt: row.CreatedAt}
		return nil
	})
	if err != nil {
		return MemberDTO{}, fmt.Errorf("add team member: %w", err)
	}
	return member, nil
}

func (s *Service) RemoveMember(ctx context.Context, userID, orgID, teamID, teamMemberID uuid.UUID) error {
	if _, err := s.authorize(ctx, userID, orgID, organizations.PermissionTeamsManage); err != nil {
		return err
	}
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.GetTeam(ctx, db.GetTeamParams{ID: teamID, OrganizationID: orgID}); errors.Is(err, pgx.ErrNoRows) {
			return httpx.ErrNotFound
		} else if err != nil {
			return err
		}
		return q.RemoveTeamMember(ctx, db.RemoveTeamMemberParams{ID: teamMemberID, TeamID: teamID, OrganizationID: orgID})
	})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
