// Package organizations implements Phase 3 multi-tenancy: organizations,
// memberships, tenant resolution, and organization switching.
package organizations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
)

// System roles seeded for every organization.
var defaultRoles = []struct {
	name string
	desc string
}{
	{"Owner", "Full control over the organization"},
	{"Admin", "Manage organization resources and members"},
	{"Manager", "Manage teams, projects and tasks"},
	{"Member", "Work on assigned projects and tasks"},
	{"Viewer", "Read-only access"},
}

// Service owns organization use cases.
type Service struct {
	pool   *pgxpool.Pool
	q      *db.Queries
	logger *slog.Logger
}

// NewService constructs the organizations Service.
func NewService(pool *pgxpool.Pool, logger *slog.Logger) *Service {
	return &Service{pool: pool, q: db.New(pool), logger: logger}
}

// OrganizationDTO is the client-safe organization representation.
type OrganizationDTO struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	Role      string    `json:"role,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MemberDTO is a client-safe membership row.
type MemberDTO struct {
	MembershipID uuid.UUID  `json:"membership_id"`
	UserID       uuid.UUID  `json:"user_id"`
	Email        string     `json:"email"`
	FirstName    string     `json:"first_name"`
	LastName     string     `json:"last_name"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	JoinedAt     *time.Time `json:"joined_at"`
}

// withOrgTx runs fn inside a transaction with app.current_org_id set to orgID
// for that transaction. RLS policies then confine tenant-scoped statements to
// the active organization, backstopping the application-level WHERE clauses.
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

// ResolveTenant verifies userID has an active membership in orgID and that the
// organization is active. Returns 404 (not 403) for unknown/inaccessible orgs
// to avoid leaking organization existence across tenants. Reads run under the
// tenant RLS context as defense in depth.
func (s *Service) ResolveTenant(ctx context.Context, userID, orgID uuid.UUID) (authctx.Tenant, error) {
	var tenant authctx.Tenant
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.GetMembership(ctx, db.GetMembershipParams{
			OrganizationID: orgID,
			UserID:         userID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", err)
			}
			return fmt.Errorf("get membership: %w", err)
		}
		if row.Status != "active" {
			return httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", nil)
		}
		org, err := q.GetOrganizationByID(ctx, orgID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", err)
			}
			return fmt.Errorf("get organization: %w", err)
		}
		if org.Status == "deleted" {
			return httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", nil)
		}
		if org.Status != "active" {
			return httpx.NewAPIError(403, "ORGANIZATION_SUSPENDED", "Organization is not active", nil)
		}
		role, err := q.GetRoleByID(ctx, db.GetRoleByIDParams{ID: row.RoleID, OrganizationID: orgID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", err)
			}
			return fmt.Errorf("get role: %w", err)
		}
		tenant = authctx.Tenant{
			OrganizationID: orgID,
			MembershipID:   row.ID,
			RoleID:         row.RoleID,
			RoleName:       role.Name,
		}
		return nil
	})
	if err != nil {
		return authctx.Tenant{}, err
	}
	return tenant, nil
}

// ListMyOrganizations returns active organizations the user belongs to.
func (s *Service) ListMyOrganizations(ctx context.Context, userID uuid.UUID) ([]OrganizationDTO, error) {
	rows, err := s.q.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	out := make([]OrganizationDTO, 0, len(rows))
	for _, r := range rows {
		if r.OrganizationStatus != "active" {
			continue
		}
		out = append(out, OrganizationDTO{
			ID: r.OrganizationID, Name: r.OrganizationName, Slug: r.OrganizationSlug,
			Status: r.OrganizationStatus, Role: r.RoleName,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}

// GetOrganization returns the org only if the caller is an active member
// (tenant already resolved by middleware, re-verified here for service-level
// authorization defense in depth).
func (s *Service) GetOrganization(ctx context.Context, userID, orgID uuid.UUID) (OrganizationDTO, error) {
	t, err := s.ResolveTenant(ctx, userID, orgID)
	if err != nil {
		return OrganizationDTO{}, err
	}
	var org db.Organization
	err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		o, e := s.q.WithTx(tx).GetOrganizationByID(ctx, orgID)
		if e != nil {
			return fmt.Errorf("get organization: %w", e)
		}
		org = o
		return nil
	})
	if err != nil {
		return OrganizationDTO{}, err
	}
	return OrganizationDTO{ID: org.ID, Name: org.Name, Slug: org.Slug, Status: org.Status,
		Role: t.RoleName, CreatedAt: org.CreatedAt, UpdatedAt: org.UpdatedAt}, nil
}

// CreateOrganization creates an org + all default system roles + owner
// membership for the caller, atomically.
func (s *Service) CreateOrganization(ctx context.Context, userID uuid.UUID, name string) (OrganizationDTO, error) {
	slug, err := s.uniqueSlug(ctx, name)
	if err != nil {
		return OrganizationDTO{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return OrganizationDTO{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	org, err := qtx.CreateOrganization(ctx, db.CreateOrganizationParams{
		Name: name, Slug: slug, Status: "active",
	})
	if err != nil {
		return OrganizationDTO{}, fmt.Errorf("create organization: %w", err)
	}
	var ownerID uuid.UUID
	for _, r := range defaultRoles {
		desc := r.desc
		role, err := qtx.CreateRole(ctx, db.CreateRoleParams{
			OrganizationID: org.ID, Name: r.name, Description: &desc, IsSystem: true,
		})
		if err != nil {
			return OrganizationDTO{}, fmt.Errorf("create role %s: %w", r.name, err)
		}
		if r.name == "Owner" {
			ownerID = role.ID
		}
	}
	now := time.Now()
	if _, err := qtx.CreateMembership(ctx, db.CreateMembershipParams{
		OrganizationID: org.ID, UserID: userID, RoleID: ownerID, Status: "active", JoinedAt: &now,
	}); err != nil {
		return OrganizationDTO{}, fmt.Errorf("create membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrganizationDTO{}, fmt.Errorf("commit tx: %w", err)
	}
	return OrganizationDTO{ID: org.ID, Name: org.Name, Slug: org.Slug, Status: org.Status,
		Role: "Owner", CreatedAt: org.CreatedAt, UpdatedAt: org.UpdatedAt}, nil
}

// UpdateOrganization renames the org. Suspended/deleted orgs reject mutations.
func (s *Service) UpdateOrganization(ctx context.Context, userID, orgID uuid.UUID, name string) (OrganizationDTO, error) {
	t, err := s.ResolveTenant(ctx, userID, orgID)
	if err != nil {
		return OrganizationDTO{}, err
	}
	if t.RoleName != "Owner" && t.RoleName != "Admin" {
		return OrganizationDTO{}, httpx.ErrForbidden
	}
	var org db.Organization
	err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE organizations SET name = $2 WHERE id = $1 AND status = 'active' RETURNING id, name, slug, status, created_at, updated_at`,
			orgID, name).Scan(&org.ID, &org.Name, &org.Slug, &org.Status, &org.CreatedAt, &org.UpdatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrganizationDTO{}, httpx.NewAPIError(404, "ORGANIZATION_NOT_FOUND", "Organization not found", err)
		}
		return OrganizationDTO{}, fmt.Errorf("update organization: %w", err)
	}
	return OrganizationDTO{ID: org.ID, Name: org.Name, Slug: org.Slug, Status: org.Status,
		Role: t.RoleName, CreatedAt: org.CreatedAt, UpdatedAt: org.UpdatedAt}, nil
}

// ListMembers returns org members; any active member may list (read).
func (s *Service) ListMembers(ctx context.Context, userID, orgID uuid.UUID) ([]MemberDTO, error) {
	if _, err := s.ResolveTenant(ctx, userID, orgID); err != nil {
		return nil, err
	}
	var rows []db.ListMembersByOrganizationRow
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		r, e := s.q.WithTx(tx).ListMembersByOrganization(ctx, orgID)
		if e != nil {
			return fmt.Errorf("list members: %w", e)
		}
		rows = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]MemberDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, MemberDTO{
			MembershipID: r.ID, UserID: r.UserID, Email: r.Email,
			FirstName: r.FirstName, LastName: r.LastName,
			Role: r.RoleName, Status: r.Status, JoinedAt: r.JoinedAt,
		})
	}
	return out, nil
}

func (s *Service) uniqueSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	candidate := base
	for i := 0; i < 5; i++ {
		exists, err := s.q.OrganizationSlugExists(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("check slug: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%s", base, uuid.New().String()[:8])
	}
	return candidate, nil
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "org"
	}
	return out
}
