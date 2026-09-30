package invitations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/jobs"
	"github.com/itsMinar/team-flow/internal/mailer"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/permissions"
)

const (
	uniqueViolation        = "23505"
	pendingEmailConstraint = "idx_invitations_pending_email"
	userEmailConstraint    = "users_email_key"
	membershipEmailUnique  = "organization_memberships_organization_id_user_id_key"
	activeMembershipStatus = "active"
	activeUserStatus       = "active"
)

// Service owns invitation use cases: creating, resending, revoking, previewing,
// and accepting organization invitations.
type Service struct {
	pool    *pgxpool.Pool
	q       *db.Queries
	orgs    *organizations.Service
	auth    *auth.Service
	mailer  mailer.Sender
	queue   jobs.Enqueuer
	logger  *slog.Logger
	ttl     time.Duration
	baseURL string
}

// NewService constructs the invitations Service. The queue is optional: without
// one, invitation emails are delivered inline instead of through a worker.
func NewService(pool *pgxpool.Pool, orgs *organizations.Service, authSvc *auth.Service,
	sender mailer.Sender, queue jobs.Enqueuer, ttl time.Duration, baseURL string, logger *slog.Logger,
) *Service {
	return &Service{
		pool: pool, q: db.New(pool), orgs: orgs, auth: authSvc, mailer: sender, queue: queue,
		ttl: ttl, baseURL: strings.TrimRight(baseURL, "/"), logger: logger,
	}
}

// withOrgTx runs fn inside a transaction whose RLS context is confined to orgID.
func (s *Service) withOrgTx(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	return database.WithTenantTx(ctx, s.pool, orgID, fn)
}

func notFound(err error) error {
	return httpx.NewAPIError(404, "INVITATION_NOT_FOUND", "Invitation not found", err)
}

func roleNotFound(err error) error {
	return httpx.NewAPIError(404, "ROLE_NOT_FOUND", "Role not found", err)
}

// unusable maps an invitation that can no longer be accepted to a safe error.
// The distinctions (expired, revoked, already used) only ever reach someone
// holding a valid token, so they help the invitee without disclosing anything
// about the organization.
func unusable(inv db.Invitation, now time.Time) error {
	switch effectiveStatus(inv, now) {
	case StatusAccepted:
		return httpx.NewAPIError(409, "INVITATION_ALREADY_ACCEPTED", "This invitation has already been accepted", nil)
	case StatusRevoked:
		return httpx.NewAPIError(410, "INVITATION_REVOKED", "This invitation was revoked", nil)
	case StatusExpired:
		return httpx.NewAPIError(410, "INVITATION_EXPIRED", "This invitation has expired", nil)
	}
	return notFound(nil)
}

// Create invites an email address to the organization with the given role.
//
// The raw token is emailed and never persisted or returned in the API response:
// a caller with members.manage must not be able to redeem an invitation
// addressed to someone else.
func (s *Service) Create(ctx context.Context, userID, orgID uuid.UUID, in CreateInput) (InvitationDTO, error) {
	tenant, err := s.orgs.Authorize(ctx, userID, orgID, permissions.MembersManage)
	if err != nil {
		return InvitationDTO{}, err
	}
	if err := in.validate(); err != nil {
		return InvitationDTO{}, err
	}
	email := normalizeEmail(in.Email)

	token, err := generateToken()
	if err != nil {
		return InvitationDTO{}, err
	}
	now := time.Now()
	expiresAt := now.Add(s.ttl)

	var invitation db.Invitation
	var roleName, orgName string
	err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := s.checkInvitee(ctx, q, orgID, email); err != nil {
			return err
		}
		role, err := q.GetRoleByID(ctx, db.GetRoleByIDParams{ID: in.RoleID, OrganizationID: orgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return roleNotFound(err)
		} else if err != nil {
			return err
		}
		// Handing out the Owner role is reserved for existing Owners, matching
		// role assignment for existing members.
		if role.Name == ownerRoleName && tenant.RoleName != ownerRoleName {
			return httpx.ErrForbidden
		}
		if orgName, err = s.orgName(ctx, q, orgID); err != nil {
			return err
		}
		invitation, err = q.CreateInvitation(ctx, db.CreateInvitationParams{
			OrganizationID: orgID, Email: email, RoleID: in.RoleID,
			TokenHash: hashToken(token), InvitedBy: userID, ExpiresAt: expiresAt,
		})
		if err != nil {
			return mapWriteError(err)
		}
		roleName = role.Name
		return nil
	})
	if err != nil {
		return InvitationDTO{}, fmt.Errorf("create invitation: %w", err)
	}

	dto := toDTO(invitation, roleName, now)
	// Delivery is dispatched after the transaction commits so a message can never
	// reference an invitation that was rolled back.
	s.dispatchEmail(ctx, invitation, dto, orgName, token)
	return dto, nil
}

// dispatchEmail hands the invitation email to the worker, falling back to inline
// delivery when no queue is configured or the queue is unavailable.
//
// The fallback is what keeps a Redis outage from silently swallowing invitations,
// and the redelivery sweep is the second safety net for the case where both the
// queue and the inline attempt fail.
func (s *Service) dispatchEmail(ctx context.Context, invitation db.Invitation, dto InvitationDTO, orgName, token string) {
	if s.queue != nil {
		if _, err := s.enqueueEmail(ctx, invitation, token); err != nil {
			s.logger.Error("failed to queue invitation email, delivering inline",
				slog.String("invitation_id", dto.ID.String()),
				slog.Any("error", err),
			)
			s.deliverInline(ctx, invitation, dto, orgName, token)
			return
		}
		return
	}
	s.deliverInline(ctx, invitation, dto, orgName, token)
}

// deliverInline sends the email in the request and records it, so an invitation
// delivered without a worker is not picked up by the redelivery sweep later.
func (s *Service) deliverInline(ctx context.Context, invitation db.Invitation, dto InvitationDTO, orgName, token string) {
	s.sendInvitationEmail(ctx, invitation, dto, orgName, token)
	if err := s.markNotified(ctx, invitation.ID, invitation.OrganizationID); err != nil {
		s.logger.Error("failed to record inline invitation delivery",
			slog.String("invitation_id", invitation.ID.String()),
			slog.Any("error", err))
	}
}

// checkInvitee rejects invitations for someone who already belongs to the
// organization, including the inviter themselves.
func (s *Service) checkInvitee(ctx context.Context, q *db.Queries, orgID uuid.UUID, email string) error {
	_, err := q.GetActiveMembershipByEmail(ctx, db.GetActiveMembershipByEmailParams{
		OrganizationID: orgID, Email: email,
	})
	if err == nil {
		return httpx.NewAPIError(409, "ALREADY_A_MEMBER", "This person is already a member of the organization", nil)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// Resend rotates the token of a pending invitation and emails the new link. The
// previous link stops working immediately, which is what makes a lost email
// recoverable without ever storing the token.
func (s *Service) Resend(ctx context.Context, userID, orgID, invitationID uuid.UUID) (InvitationDTO, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.MembersManage); err != nil {
		return InvitationDTO{}, err
	}
	token, err := generateToken()
	if err != nil {
		return InvitationDTO{}, err
	}
	now := time.Now()
	expiresAt := now.Add(s.ttl)

	var invitation db.Invitation
	var roleName, orgName string
	err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := q.GetInvitationByIDForUpdate(ctx, db.GetInvitationByIDForUpdateParams{
			ID: invitationID, OrganizationID: orgID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(err)
		} else if err != nil {
			return err
		}
		if current.Status != StatusPending {
			return httpx.NewAPIError(409, "INVITATION_NOT_ACTIVE", "Only pending invitations can be resent", nil)
		}
		role, err := q.GetRoleByID(ctx, db.GetRoleByIDParams{ID: current.RoleID, OrganizationID: orgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return roleNotFound(err)
		} else if err != nil {
			return err
		}
		if orgName, err = s.orgName(ctx, q, orgID); err != nil {
			return err
		}
		roleName = role.Name
		invitation, err = q.RotateInvitationToken(ctx, db.RotateInvitationTokenParams{
			ID: invitationID, OrganizationID: orgID, TokenHash: hashToken(token), ExpiresAt: expiresAt,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewAPIError(409, "INVITATION_NOT_ACTIVE", "Only pending invitations can be resent", nil)
		}
		return err
	})
	if err != nil {
		return InvitationDTO{}, fmt.Errorf("resend invitation: %w", err)
	}

	dto := toDTO(invitation, roleName, now)
	s.dispatchEmail(ctx, invitation, dto, orgName, token)
	return dto, nil
}

// Revoke invalidates a pending invitation. Invitations that are already accepted
// or revoked are reported as not found, so revoking cannot be used to inspect
// invitation history.
func (s *Service) Revoke(ctx context.Context, userID, orgID, invitationID uuid.UUID) error {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.MembersManage); err != nil {
		return err
	}
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := q.GetInvitationByIDForUpdate(ctx, db.GetInvitationByIDForUpdateParams{
			ID: invitationID, OrganizationID: orgID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(err)
		} else if err != nil {
			return err
		}
		if current.Status != StatusPending {
			return notFound(nil)
		}
		now := time.Now()
		_, err = q.RevokeInvitation(ctx, db.RevokeInvitationParams{
			ID: invitationID, OrganizationID: orgID, RevokedAt: &now,
		})
		return err
	})
}

// List returns the organization's invitations, newest first by default.
func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]InvitationDTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.MembersManage); err != nil {
		return nil, httpx.Pagination{}, err
	}
	sortBy := filter.Sort
	if !sortableFields[sortBy] {
		sortBy = defaultSort
	}
	var rows []db.Invitation
	var total int64
	roleNames := map[uuid.UUID]string{}
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		params := db.CountInvitationsParams{
			OrganizationID: orgID, Status: filter.Status, Email: filter.Email,
		}
		var err error
		if total, err = q.CountInvitations(ctx, params); err != nil {
			return err
		}
		rows, err = q.ListInvitations(ctx, db.ListInvitationsParams{
			OrganizationID: orgID, Status: filter.Status, Email: filter.Email,
			SortBy: sortBy, SortDesc: filter.Desc, PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		if err != nil {
			return err
		}
		// Resolve each distinct role once instead of per row.
		for _, row := range rows {
			if _, ok := roleNames[row.RoleID]; ok {
				continue
			}
			role, err := q.GetRoleByID(ctx, db.GetRoleByIDParams{ID: row.RoleID, OrganizationID: orgID})
			if err != nil {
				return fmt.Errorf("get role: %w", err)
			}
			roleNames[row.RoleID] = role.Name
		}
		return nil
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list invitations: %w", err)
	}
	now := time.Now()
	result := make([]InvitationDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, toDTO(row, roleNames[row.RoleID], now))
	}
	return result, httpx.NewPagination(page, total), nil
}

// orgName reads the organization name inside the caller's transaction.
func (s *Service) orgName(ctx context.Context, q *db.Queries, orgID uuid.UUID) (string, error) {
	org, err := q.GetOrganizationByID(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("get organization: %w", err)
	}
	return org.Name, nil
}

// Preview describes an invitation for the public accept page. It exposes only
// what the emailed invitee already knows and never returns the token or any
// other organization data.
func (s *Service) Preview(ctx context.Context, token string) (PreviewDTO, error) {
	invitation, org, role, err := s.lookup(ctx, token)
	if err != nil {
		return PreviewDTO{}, err
	}
	accountExists := false
	if _, err := s.q.GetUserByEmail(ctx, invitation.Email); err == nil {
		accountExists = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PreviewDTO{}, fmt.Errorf("check invitee account: %w", err)
	}
	return PreviewDTO{
		OrganizationID: org.ID, OrganizationName: org.Name, Email: invitation.Email,
		RoleName: role.Name, Status: effectiveStatus(invitation, time.Now()),
		ExpiresAt: invitation.ExpiresAt, AccountExists: accountExists,
	}, nil
}

// Accept completes an invitation.
//
// When userID is set the caller already has an account and that account's email
// must match the invitation; otherwise the invitation creates a new account from
// the supplied credentials. Both paths run inside one tenant-scoped transaction
// that creates the membership and marks the invitation accepted, so a membership
// can never exist without a matching accepted invitation.
func (s *Service) Accept(ctx context.Context, userID uuid.UUID, in AcceptInput, meta auth.RequestMeta) (AcceptResult, error) {
	token := strings.TrimSpace(in.Token)
	if token == "" {
		return AcceptResult{}, httpx.NewValidationError(map[string]string{"token": "is required"})
	}
	if err := in.validateAccount(); err != nil {
		return AcceptResult{}, err
	}

	// Hashing before the transaction keeps the row lock short.
	var passwordHash string
	if userID == uuid.Nil {
		if in.Password == "" {
			return AcceptResult{}, httpx.NewValidationError(map[string]string{
				"password": "is required when accepting without an account",
			})
		}
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			return AcceptResult{}, err
		}
		passwordHash = hash
	}

	// The token identifies the organization, so it is resolved without a tenant
	// context and every later statement runs inside that tenant's transaction.
	found, err := s.q.GetInvitationByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return AcceptResult{}, notFound(err)
	} else if err != nil {
		return AcceptResult{}, fmt.Errorf("get invitation by token: %w", err)
	}

	var (
		org     db.Organization
		role    db.Role
		user    db.User
		now     = time.Now()
		expired bool
	)
	err = s.withOrgTx(ctx, found.OrganizationID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		invitation, err := q.GetInvitationByTokenHashForUpdate(ctx, hashToken(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(err)
		} else if err != nil {
			return err
		}
		if !isUsable(invitation, now) {
			if invitation.Status != StatusPending {
				return unusable(invitation, now)
			}
			// Persist the expiry transition, then let the transaction commit and
			// report the failure afterwards; returning it here would roll the
			// update back.
			updated, err := q.MarkInvitationExpired(ctx, db.MarkInvitationExpiredParams{
				ID: invitation.ID, OrganizationID: invitation.OrganizationID,
			})
			if err != nil {
				return fmt.Errorf("mark invitation expired: %w", err)
			}
			invitation, expired = updated, true
			return nil
		}

		if role, err = q.GetRoleByID(ctx, db.GetRoleByIDParams{
			ID: invitation.RoleID, OrganizationID: invitation.OrganizationID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roleNotFound(err)
			}
			return err
		}
		if org, err = q.GetOrganizationByID(ctx, invitation.OrganizationID); err != nil {
			return fmt.Errorf("get organization: %w", err)
		}
		if user, err = s.resolveUser(ctx, q, invitation, userID, in, passwordHash); err != nil {
			return err
		}
		joinedAt := now
		if _, err := q.CreateMembership(ctx, db.CreateMembershipParams{
			OrganizationID: invitation.OrganizationID, UserID: user.ID, RoleID: invitation.RoleID,
			Status: activeMembershipStatus, JoinedAt: &joinedAt,
		}); err != nil {
			return mapMembershipError(err)
		}
		if _, err = q.AcceptInvitation(ctx, db.AcceptInvitationParams{
			ID: invitation.ID, OrganizationID: invitation.OrganizationID,
			AcceptedAt: &now, AcceptedBy: &user.ID,
		}); err != nil {
			return fmt.Errorf("accept invitation: %w", err)
		}
		return nil
	})
	if err != nil {
		return AcceptResult{}, err
	}
	if expired {
		return AcceptResult{}, unusable(found, now)
	}

	tokens, err := s.auth.IssueSession(ctx, user.ID, meta)
	if err != nil {
		return AcceptResult{}, err
	}
	return AcceptResult{
		InvitationID: found.ID, OrganizationID: org.ID, OrganizationName: org.Name,
		RoleName: role.Name, User: auth.NewUserDTO(user), Tokens: *tokens,
	}, nil
}

// resolveUser returns the account that accepts the invitation: the existing
// authenticated account, or a newly created one. The email must match the
// invitation in both cases.
func (s *Service) resolveUser(ctx context.Context, q *db.Queries, invitation db.Invitation,
	userID uuid.UUID, in AcceptInput, passwordHash string,
) (db.User, error) {
	if userID != uuid.Nil {
		user, err := q.GetUserByID(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return db.User{}, httpx.ErrUnauthorized
		} else if err != nil {
			return db.User{}, err
		}
		if user.Status != activeUserStatus {
			return db.User{}, httpx.ErrUnauthorized
		}
		// The caller proved they hold the token but not that it was issued to
		// them; refusing here stops one account from redeeming another account's
		// invitation.
		if normalizeEmail(user.Email) != normalizeEmail(invitation.Email) {
			return db.User{}, httpx.NewAPIError(403, "INVITATION_EMAIL_MISMATCH",
				"This invitation was issued to a different email address", nil)
		}
		return user, nil
	}

	existing, err := q.GetUserByEmail(ctx, invitation.Email)
	if err == nil {
		if existing.Status != activeUserStatus {
			return db.User{}, httpx.ErrUnauthorized
		}
		return db.User{}, httpx.NewAPIError(409, "ACCOUNT_EXISTS",
			"An account with this email already exists; sign in to accept this invitation", nil)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.User{}, err
	}

	created, err := q.CreateUser(ctx, db.CreateUserParams{
		Email: normalizeEmail(invitation.Email), PasswordHash: passwordHash,
		FirstName: strings.TrimSpace(in.FirstName), LastName: strings.TrimSpace(in.LastName),
		Status: activeUserStatus,
	})
	if err != nil {
		return db.User{}, mapWriteError(err)
	}
	return created, nil
}

// lookup resolves an invitation by token for read-only use, together with its
// organization and role.
func (s *Service) lookup(ctx context.Context, token string) (db.Invitation, db.Organization, db.Role, error) {
	var (
		invitation db.Invitation
		org        db.Organization
		role       db.Role
	)
	found, err := s.q.GetInvitationByTokenHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return invitation, org, role, notFound(err)
	} else if err != nil {
		return invitation, org, role, fmt.Errorf("get invitation by token: %w", err)
	}
	err = s.withOrgTx(ctx, found.OrganizationID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if invitation, err = q.GetInvitationByID(ctx, db.GetInvitationByIDParams{
			ID: found.ID, OrganizationID: found.OrganizationID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return notFound(err)
			}
			return err
		}
		if org, err = q.GetOrganizationByID(ctx, invitation.OrganizationID); err != nil {
			return fmt.Errorf("get organization: %w", err)
		}
		if role, err = q.GetRoleByID(ctx, db.GetRoleByIDParams{
			ID: invitation.RoleID, OrganizationID: invitation.OrganizationID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return roleNotFound(err)
			}
			return err
		}
		return nil
	})
	if err != nil {
		return db.Invitation{}, db.Organization{}, db.Role{}, err
	}
	return invitation, org, role, nil
}

// sendInvitationEmail hands the invitation link to the configured transport.
//
// A delivery failure is logged rather than returned: the invitation exists and
// can be resent, while reporting an error would suggest the invite was never
// created. On the worker path the job still fails and is retried, so a temporary
// transport outage does not lose the message.
func (s *Service) sendInvitationEmail(ctx context.Context, invitation db.Invitation, dto InvitationDTO, orgName, token string) {
	link := acceptURL(s.baseURL, token)
	msg := mailer.Message{
		To:      invitation.Email,
		Subject: fmt.Sprintf("You have been invited to join %s on TeamFlow", orgName),
		Link:    link,
		Text: fmt.Sprintf("You have been invited to join %s as %s.\n\nAccept the invitation:\n%s\n\n"+
			"This link expires at %s and can only be used once.\n",
			orgName, dto.RoleName, link, invitation.ExpiresAt.UTC().Format(time.RFC3339)),
		Metadata: map[string]string{
			"invitation_id":   dto.ID.String(),
			"organization_id": invitation.OrganizationID.String(),
			"expires_at":      invitation.ExpiresAt.UTC().Format(time.RFC3339),
		},
	}
	if err := s.mailer.Send(ctx, msg); err != nil {
		s.logger.Error("failed to deliver invitation email",
			slog.String("invitation_id", dto.ID.String()),
			slog.Any("error", err),
		)
	}
}

// mapWriteError converts database constraint violations into safe domain errors.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		return err
	}
	switch pgErr.ConstraintName {
	case pendingEmailConstraint:
		return httpx.NewAPIError(409, "INVITATION_PENDING", "This email already has a pending invitation", err)
	case userEmailConstraint:
		return httpx.NewAPIError(409, "EMAIL_TAKEN", "An account with this email already exists", err)
	}
	return err
}

// mapMembershipError keeps a concurrent double acceptance from surfacing a raw
// unique violation.
func mapMembershipError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewAPIError(409, "ALREADY_A_MEMBER", "This person is already a member of the organization", err)
	}
	return err
}
