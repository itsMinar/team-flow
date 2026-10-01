package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/permissions"
)

// Authorizer resolves whether a user may act in an organization. It is an
// interface rather than the organizations service so this package stays free of a
// dependency cycle: the organizations service mounts routes through the auth
// package, which records audit events through this one.
type Authorizer interface {
	Authorize(ctx context.Context, userID, orgID uuid.UUID, permission string) (authctx.Tenant, error)
}

// ListFilter narrows an audit log query. Every field is optional.
type ListFilter struct {
	Action      *string
	Outcome     *string
	ActorUserID *uuid.UUID
	Since       *time.Time
}

// Service reads the audit log. Writing is handled by Recorder.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	authorizer Authorizer
}

// NewService builds the audit read service.
func NewService(pool *pgxpool.Pool, authorizer Authorizer) *Service {
	return &Service{pool: pool, q: db.New(pool), authorizer: authorizer}
}

// List returns an organization's audit entries, newest first.
//
// Reading requires audit.read, and every query is scoped by organization inside a
// tenant transaction so neither the authorization check nor RLS can be bypassed by
// a crafted parameter.
func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID,
	filter ListFilter, page httpx.PageRequest,
) ([]Entry, httpx.Pagination, error) {
	if _, err := s.authorizer.Authorize(ctx, userID, orgID, permissions.AuditRead); err != nil {
		return nil, httpx.Pagination{}, err
	}
	var rows []db.ListAuditLogsRow
	var total int64
	org := orgID
	err := database.WithTenantTx(ctx, s.pool, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		params := db.CountAuditLogsParams{
			OrganizationID: &org, Action: filter.Action,
			Outcome: filter.Outcome, ActorUserID: filter.ActorUserID, Since: filter.Since,
		}
		var err error
		if total, err = q.CountAuditLogs(ctx, params); err != nil {
			return err
		}
		rows, err = q.ListAuditLogs(ctx, db.ListAuditLogsParams{
			OrganizationID: &org, Action: filter.Action, Outcome: filter.Outcome,
			ActorUserID: filter.ActorUserID, Since: filter.Since,
			PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list audit logs: %w", err)
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, ToEntry(row))
	}
	return entries, httpx.NewPagination(page, total), nil
}

// KnownActions is the set of actions this build can emit. It exists so clients and
// documentation have one place to look, and so a typo in an action name is caught.
var KnownActions = []string{
	AuthRegistered, AuthLoginSucceeded, AuthLoginFailed, AuthTokensRefreshed,
	AuthTokenReuseDetected, AuthLoggedOut, AuthLoggedOutAll,
	OrganizationCreated, OrganizationRenamed, RoleCreated, RoleUpdated, RoleDeleted,
	MemberRoleAssigned,
	InvitationCreated, InvitationSent, InvitationResent, InvitationRevoked, InvitationAccepted,
	APIKeyCreated, APIKeyRevoked,
}

// IsKnownAction reports whether an action is part of the documented set. Unknown
// actions are still stored: the log must record what actually happened even when a
// newer release adds actions this build does not know about.
func IsKnownAction(action string) bool {
	for _, known := range KnownActions {
		if known == action {
			return true
		}
	}
	return false
}
