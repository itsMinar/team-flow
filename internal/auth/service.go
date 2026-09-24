package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
)

const (
	ownerRoleName    = "Owner"
	uniqueViolation  = "23505"
	maxSlugAttempts  = 5
	activeUserStatus = "active"
)

// Service implements the authentication use cases: registration, login, token
// refresh with rotation/reuse-detection, and logout.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	jwt        *JWTService
	refreshTTL time.Duration
	logger     *slog.Logger
}

// NewService constructs the auth Service.
func NewService(pool *pgxpool.Pool, jwt *JWTService, refreshTTL time.Duration, logger *slog.Logger) *Service {
	return &Service{
		pool:       pool,
		q:          db.New(pool),
		jwt:        jwt,
		refreshTTL: refreshTTL,
		logger:     logger,
	}
}

// Register creates a user, their organization, an Owner role, and an active
// membership atomically. If any step fails the whole transaction rolls back.
func (s *Service) Register(ctx context.Context, in RegisterInput, meta RequestMeta) (*AuthResult, error) {
	passwordHash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err
	}

	slug, err := s.uniqueSlug(ctx, in.OrganizationName)
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)

	user, err := qtx.CreateUser(ctx, db.CreateUserParams{
		Email:        in.Email,
		PasswordHash: passwordHash,
		FirstName:    in.FirstName,
		LastName:     in.LastName,
		Status:       activeUserStatus,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, httpx.NewAPIError(409, "EMAIL_TAKEN", "An account with this email already exists", err)
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	org, err := qtx.CreateOrganization(ctx, db.CreateOrganizationParams{
		Name:   in.OrganizationName,
		Slug:   slug,
		Status: activeUserStatus,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, httpx.NewAPIError(409, "SLUG_TAKEN", "Organization name is unavailable", err)
		}
		return nil, fmt.Errorf("create organization: %w", err)
	}

	ownerDesc := "Full control over the organization"
	role, err := qtx.CreateRole(ctx, db.CreateRoleParams{
		OrganizationID: org.ID,
		Name:           ownerRoleName,
		Description:    &ownerDesc,
		IsSystem:       true,
	})
	if err != nil {
		return nil, fmt.Errorf("create owner role: %w", err)
	}
	if err := seedRolePermissions(ctx, qtx, role.ID, ownerRoleName); err != nil {
		return nil, fmt.Errorf("seed owner permissions: %w", err)
	}
	// Seed remaining default system roles so Phase 4 RBAC has them.
	for _, extra := range []struct{ name, desc string }{
		{"Admin", "Manage organization resources and members"},
		{"Manager", "Manage teams, projects and tasks"},
		{"Member", "Work on assigned projects and tasks"},
		{"Viewer", "Read-only access"},
	} {
		desc := extra.desc
		extraRole, err := qtx.CreateRole(ctx, db.CreateRoleParams{
			OrganizationID: org.ID, Name: extra.name, Description: &desc, IsSystem: true,
		})
		if err != nil {
			return nil, fmt.Errorf("create role %s: %w", extra.name, err)
		}
		if err := seedRolePermissions(ctx, qtx, extraRole.ID, extra.name); err != nil {
			return nil, fmt.Errorf("seed permissions for role %s: %w", extra.name, err)
		}
	}

	now := time.Now()
	if _, err := qtx.CreateMembership(ctx, db.CreateMembershipParams{
		OrganizationID: org.ID,
		UserID:         user.ID,
		RoleID:         role.ID,
		Status:         activeUserStatus,
		JoinedAt:       &now,
	}); err != nil {
		return nil, fmt.Errorf("create membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	tokens, err := s.issueSession(ctx, user.ID, uuid.New(), meta)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Tokens: *tokens, User: newUserDTO(user)}, nil
}

// Login verifies credentials and issues a new session. It returns a generic
// unauthorized error for any failure to avoid user enumeration.
func (s *Service) Login(ctx context.Context, in LoginInput, meta RequestMeta) (*AuthResult, error) {
	user, err := s.q.GetUserByEmail(ctx, in.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Run a dummy verify to reduce timing side-channels, then fail.
			_ = VerifyPassword("$2a$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidin", in.Password)
			return nil, invalidCredentials()
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}

	if user.Status != activeUserStatus {
		return nil, invalidCredentials()
	}
	if !VerifyPassword(user.PasswordHash, in.Password) {
		return nil, invalidCredentials()
	}

	if err := s.q.UpdateUserLastLogin(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("update last login: %w", err)
	}

	tokens, err := s.issueSession(ctx, user.ID, uuid.New(), meta)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Tokens: *tokens, User: newUserDTO(user)}, nil
}

// Refresh rotates a refresh token: it validates the presented token, issues a
// new token in the same family, and revokes the old one. Presenting an already
// revoked token is treated as reuse and revokes the entire family.
func (s *Service) Refresh(ctx context.Context, rawToken string, meta RequestMeta) (*AuthResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	stored, err := qtx.GetRefreshTokenByHashForUpdate(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.ErrUnauthorized
		}
		return nil, fmt.Errorf("get refresh token: %w", err)
	}

	// Reuse detection: a revoked token being presented again means the token
	// was stolen or replayed. Revoke the whole family and reject.
	if stored.RevokedAt != nil {
		if err := qtx.RevokeRefreshTokenFamily(ctx, stored.FamilyID); err != nil {
			return nil, fmt.Errorf("revoke token family on reuse: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit token family revocation: %w", err)
		}
		s.logger.Warn("refresh token reuse detected",
			slog.String("user_id", stored.UserID.String()),
			slog.String("family_id", stored.FamilyID.String()),
		)
		return nil, httpx.ErrUnauthorized
	}

	if time.Now().After(stored.ExpiresAt) {
		return nil, httpx.ErrUnauthorized
	}

	user, err := qtx.GetUserByID(ctx, stored.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if user.Status != activeUserStatus {
		return nil, httpx.ErrUnauthorized
	}

	newRefresh, err := s.createRefreshToken(ctx, qtx, user.ID, stored.FamilyID, meta)
	if err != nil {
		return nil, err
	}
	if err := qtx.RevokeRefreshToken(ctx, db.RevokeRefreshTokenParams{
		ID:         stored.ID,
		ReplacedBy: &newRefresh.id,
	}); err != nil {
		return nil, fmt.Errorf("revoke old refresh token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	access, expiresAt, err := s.jwt.GenerateAccessToken(user.ID, uuid.New())
	if err != nil {
		return nil, err
	}
	return &AuthResult{
		Tokens: TokenPair{
			AccessToken:  access,
			RefreshToken: newRefresh.raw,
			ExpiresIn:    int(time.Until(expiresAt).Seconds()),
			ExpiresAt:    expiresAt,
		},
		User: newUserDTO(user),
	}, nil
}

// Logout revokes the family of the presented refresh token, ending that session
// lineage. An unknown token is treated as already logged out (no error).
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	stored, err := s.q.GetRefreshTokenByHash(ctx, hashToken(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("get refresh token: %w", err)
	}
	if err := s.q.RevokeRefreshTokenFamily(ctx, stored.FamilyID); err != nil {
		return fmt.Errorf("revoke token family: %w", err)
	}
	return nil
}

// LogoutAll revokes every active refresh token for the user, ending all
// sessions across devices.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	if err := s.q.RevokeAllUserRefreshTokens(ctx, userID); err != nil {
		return fmt.Errorf("revoke all refresh tokens: %w", err)
	}
	return nil
}

// CurrentUser returns the client-safe DTO for the given user id.
func (s *Service) CurrentUser(ctx context.Context, userID uuid.UUID) (UserDTO, error) {
	user, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserDTO{}, httpx.ErrNotFound
		}
		return UserDTO{}, fmt.Errorf("get user: %w", err)
	}
	return newUserDTO(user), nil
}

// issueSession creates a new refresh-token family and an access token.
func (s *Service) issueSession(ctx context.Context, userID, familyID uuid.UUID, meta RequestMeta) (*TokenPair, error) {
	refresh, err := s.createRefreshToken(ctx, s.q, userID, familyID, meta)
	if err != nil {
		return nil, err
	}
	access, expiresAt, err := s.jwt.GenerateAccessToken(userID, uuid.New())
	if err != nil {
		return nil, err
	}
	return &TokenPair{
		AccessToken:  access,
		RefreshToken: refresh.raw,
		ExpiresIn:    int(time.Until(expiresAt).Seconds()),
		ExpiresAt:    expiresAt,
	}, nil
}

type issuedRefresh struct {
	id  uuid.UUID
	raw string
}

// createRefreshToken generates a raw refresh token, stores only its hash, and
// returns the id and raw value. familyID groups rotated tokens together.
func (s *Service) createRefreshToken(ctx context.Context, q *db.Queries, userID, familyID uuid.UUID, meta RequestMeta) (*issuedRefresh, error) {
	raw, err := generateRefreshToken()
	if err != nil {
		return nil, err
	}
	var ua, ip *string
	if meta.UserAgent != "" {
		ua = &meta.UserAgent
	}
	if meta.IPAddress != "" {
		ip = &meta.IPAddress
	}
	row, err := q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		UserID:    userID,
		FamilyID:  familyID,
		TokenHash: hashToken(raw),
		ExpiresAt: time.Now().Add(s.refreshTTL),
		UserAgent: ua,
		IpAddress: ip,
	})
	if err != nil {
		return nil, fmt.Errorf("create refresh token: %w", err)
	}
	return &issuedRefresh{id: row.ID, raw: raw}, nil
}

// uniqueSlug derives a slug from the organization name and ensures it does not
// collide with an existing organization, appending a random suffix if needed.
func (s *Service) uniqueSlug(ctx context.Context, name string) (string, error) {
	base := slugify(name)
	candidate := base
	for i := 0; i < maxSlugAttempts; i++ {
		exists, err := s.q.OrganizationSlugExists(ctx, candidate)
		if err != nil {
			return "", fmt.Errorf("check slug: %w", err)
		}
		if !exists {
			return candidate, nil
		}
		candidate = base + "-" + randomSuffix()
	}
	return candidate, nil
}

func invalidCredentials() error {
	return httpx.NewAPIError(401, "INVALID_CREDENTIALS", "Invalid email or password", httpx.ErrUnauthorized)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func seedRolePermissions(ctx context.Context, q *db.Queries, roleID uuid.UUID, roleName string) error {
	permissions := []string{
		"organizations.read",
		"members.read",
		"roles.read",
	}
	if roleName == "Owner" || roleName == "Admin" {
		permissions = append(permissions, "organizations.update", "members.manage", "roles.manage")
	}
	for _, key := range permissions {
		permission, err := q.GetPermissionByKey(ctx, key)
		if err != nil {
			return err
		}
		if err := q.AddRolePermission(ctx, db.AddRolePermissionParams{RoleID: roleID, PermissionID: permission.ID}); err != nil {
			return err
		}
	}
	return nil
}
