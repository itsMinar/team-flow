package organizations_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/invitations"
	"github.com/itsMinar/team-flow/internal/mailer"
	"github.com/itsMinar/team-flow/internal/organizations"
)

// recordingSender captures the messages the service "delivers" so tests can act
// on the invitation link exactly as the invitee would.
type recordingSender struct {
	messages []mailer.Message
	err      error
}

func (s *recordingSender) Send(_ context.Context, msg mailer.Message) error {
	s.messages = append(s.messages, msg)
	return s.err
}

func (s *recordingSender) lastLink(t *testing.T) string {
	t.Helper()
	if len(s.messages) == 0 {
		t.Fatal("no invitation email was delivered")
	}
	link := s.messages[len(s.messages)-1].Link
	if link == "" {
		t.Fatal("delivered message has no invitation link")
	}
	return link
}

// tokenFromLink extracts the token from a delivered accept link.
func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("link %q has no token", link)
	}
	return token
}

func TestInvitationsLifecycleIsolationAndRBAC(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)
	jwt := auth.NewJWTService("integration-secret", "teamflow", 15*time.Minute)
	authSvc := auth.NewService(pool, jwt, 720*time.Hour, logger)
	sender := &recordingSender{}
	svc := invitations.NewService(pool, orgSvc, authSvc, sender, 7*24*time.Hour, "http://app.example.com", logger)
	firstPage := httpx.PageRequest{Page: 1, PageSize: httpx.DefaultPageSize}

	userA, orgA := registerOrg(t, pool, "inv-a@example.com", "Invitations Org A")
	userB, orgB := registerOrg(t, pool, "inv-b@example.com", "Invitations Org B")
	memberRole := roleIDOf(t, pool, orgA, "Member")

	t.Run("invitation creation and delivery", func(t *testing.T) {
		invitation, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{
			Email: " Invitee@Example.com ", RoleID: memberRole,
		})
		if err != nil {
			t.Fatalf("create invitation: %v", err)
		}
		if invitation.Status != invitations.StatusPending || invitation.Email != "invitee@example.com" ||
			invitation.RoleName != "Member" || invitation.InvitedBy != userA {
			t.Fatalf("unexpected invitation: %+v", invitation)
		}
		if invitation.ExpiresAt.Before(time.Now().Add(6 * 24 * time.Hour)) {
			t.Fatalf("unexpected expiry: %v", invitation.ExpiresAt)
		}
		// The link is delivered, never persisted.
		link := sender.lastLink(t)
		if !strings.HasPrefix(link, "http://app.example.com/invitations/accept?token=") {
			t.Fatalf("unexpected link %q", link)
		}
		message := sender.messages[len(sender.messages)-1]
		if message.To != "invitee@example.com" || !strings.Contains(message.Subject, "Invitations Org A") {
			t.Fatalf("unexpected message: %+v", message)
		}
		if message.Metadata["invitation_id"] != invitation.ID.String() {
			t.Fatalf("message is not traceable: %+v", message.Metadata)
		}
		var storedHash string
		if err := pool.QueryRow(ctx, `SELECT token_hash FROM invitations WHERE id = $1`, invitation.ID).Scan(&storedHash); err != nil {
			t.Fatal(err)
		}
		if storedHash == "" || storedHash == tokenFromLink(t, link) {
			t.Fatalf("the raw token must never be stored, got %q", storedHash)
		}

		preview, err := svc.Preview(ctx, tokenFromLink(t, link))
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		if preview.OrganizationName != "Invitations Org A" || preview.RoleName != "Member" ||
			preview.Status != invitations.StatusPending || preview.AccountExists {
			t.Fatalf("unexpected preview: %+v", preview)
		}

		// A second pending invitation for the same address is rejected, and an
		// existing member cannot be invited.
		_, err = svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "invitee@example.com", RoleID: memberRole})
		requireAPIError(t, err, 409, "INVITATION_PENDING")
		_, err = svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "inv-a@example.com", RoleID: memberRole})
		requireAPIError(t, err, 409, "ALREADY_A_MEMBER")
		_, err = svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "other@example.com", RoleID: uuid.New()})
		requireAPIError(t, err, 404, "ROLE_NOT_FOUND")
		// A cross-tenant role cannot be used to invite.
		_, err = svc.Create(ctx, userA, orgA, invitations.CreateInput{
			Email: "other@example.com", RoleID: roleIDOf(t, pool, orgB, "Member"),
		})
		requireAPIError(t, err, 404, "ROLE_NOT_FOUND")
		_, err = svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "not-an-email", RoleID: memberRole})
		requireAPIError(t, err, 400, "VALIDATION_ERROR")
	})

	t.Run("accepting creates the account, membership, and session", func(t *testing.T) {
		token := tokenFromLink(t, sender.lastLink(t))
		result, err := svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: token, Password: "StrongPassword123", FirstName: "Ada", LastName: "Invitee",
		}, auth.RequestMeta{})
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if result.OrganizationName != "Invitations Org A" || result.RoleName != "Member" {
			t.Fatalf("unexpected result: %+v", result)
		}
		if result.Tokens.AccessToken == "" || result.Tokens.RefreshToken == "" || result.User.ID == uuid.Nil {
			t.Fatalf("expected a ready-to-use session: %+v", result.Tokens)
		}

		// The membership is active, carries the invited role, and the caller can
		// immediately act in the organization.
		members, err := orgSvc.ListMembers(ctx, result.User.ID, orgA)
		if err != nil {
			t.Fatalf("list members: %v", err)
		}
		var found bool
		for _, member := range members {
			if member.UserID == result.User.ID {
				found = member.Role == "Member" && member.Status == "active"
			}
		}
		if !found {
			t.Fatalf("membership not created for the invitee: %+v", members)
		}
		if _, err := orgSvc.ResolveTenant(ctx, result.User.ID, orgA); err != nil {
			t.Fatalf("invitee cannot access the organization: %v", err)
		}
		// The invitation is spent.
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM invitations WHERE id = $1`, result.InvitationID).Scan(&status); err != nil || status != invitations.StatusAccepted {
			t.Fatalf("invitation status = %q, %v", status, err)
		}
		// Single use: the same link cannot be replayed.
		_, err = svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: token, Password: "StrongPassword123", FirstName: "Ada", LastName: "Invitee",
		}, auth.RequestMeta{})
		requireAPIError(t, err, 409, "INVITATION_ALREADY_ACCEPTED")

		// The issued access token is a normal session.
		claims, err := jwt.ParseAccessToken(result.Tokens.AccessToken)
		if err != nil || claims.Subject != result.User.ID.String() {
			t.Fatalf("issued token is not usable: %v", err)
		}
		// Logging out with the refresh token revokes it.
		if err := authSvc.Logout(ctx, result.Tokens.RefreshToken); err != nil {
			t.Fatalf("logout: %v", err)
		}
		if _, err := authSvc.Refresh(ctx, result.Tokens.RefreshToken, auth.RequestMeta{}); err == nil {
			t.Fatal("a revoked invitation session must not refresh")
		}
	})

	t.Run("an existing member accepts with their own session", func(t *testing.T) {
		// This account already exists but is not yet a member of org A.
		existing, _ := registerOrg(t, pool, "inv-existing@example.com", "Invitations Home E")
		invitation, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{
			Email: "inv-existing@example.com", RoleID: memberRole,
		})
		if err != nil {
			t.Fatalf("invite existing member: %v", err)
		}
		token := tokenFromLink(t, sender.lastLink(t))

		// Accepting anonymously is refused because an account already exists.
		_, err = svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: token, Password: "StrongPassword123", FirstName: "A", LastName: "B",
		}, auth.RequestMeta{})
		requireAPIError(t, err, 409, "ACCOUNT_EXISTS")

		// The matching authenticated caller can redeem it.
		result, err := svc.Accept(ctx, existing, invitations.AcceptInput{Token: token}, auth.RequestMeta{})
		if err != nil {
			t.Fatalf("authenticated accept: %v", err)
		}
		if result.User.ID != existing || result.OrganizationID != orgA || result.RoleName != "Member" {
			t.Fatalf("unexpected result: %+v", result)
		}
		tenant, err := orgSvc.ResolveTenant(ctx, existing, orgA)
		if err != nil || tenant.RoleName != "Member" {
			t.Fatalf("invitee cannot access org A: %+v, %v", tenant, err)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM invitations WHERE id = $1`, invitation.ID).Scan(&status); err != nil || status != invitations.StatusAccepted {
			t.Fatalf("invitation status = %q, %v", status, err)
		}

		// A different account cannot redeem someone else's invitation.
		other, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "inv-other@example.com", RoleID: memberRole})
		if err != nil {
			t.Fatalf("invite other: %v", err)
		}
		_ = other
		_, err = svc.Accept(ctx, existing, invitations.AcceptInput{
			Token: tokenFromLink(t, sender.lastLink(t)),
		}, auth.RequestMeta{})
		requireAPIError(t, err, 403, "INVITATION_EMAIL_MISMATCH")
	})

	t.Run("expiry is enforced and persisted", func(t *testing.T) {
		invitation, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "expired@example.com", RoleID: memberRole})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE invitations
			SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
			WHERE id = $1`, invitation.ID); err != nil {
			t.Fatal(err)
		}
		token := tokenFromLink(t, sender.lastLink(t))

		preview, err := svc.Preview(ctx, token)
		if err != nil || preview.Status != invitations.StatusExpired {
			t.Fatalf("preview must report the expiry: %+v, %v", preview, err)
		}
		listed, _, err := svc.List(ctx, userA, orgA, invitations.ListFilter{}, firstPage)
		if err != nil {
			t.Fatal(err)
		}
		for _, inv := range listed {
			if inv.ID == invitation.ID && inv.Status != invitations.StatusExpired {
				t.Fatalf("list must report the expiry: %+v", inv)
			}
		}

		_, err = svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: token, Password: "StrongPassword123", FirstName: "A", LastName: "B",
		}, auth.RequestMeta{})
		requireAPIError(t, err, 410, "INVITATION_EXPIRED")
		// The transition is persisted, and no account or membership was created.
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM invitations WHERE id = $1`, invitation.ID).Scan(&status); err != nil || status != invitations.StatusExpired {
			t.Fatalf("expiry was not persisted: %q, %v", status, err)
		}
		if _, err := authSvc.CurrentUser(ctx, uuid.Nil); err == nil {
			t.Fatal("no user should exist for the expired invitation")
		}
	})

	t.Run("resend rotates the token and revoking invalidates it", func(t *testing.T) {
		invitation, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "rotate@example.com", RoleID: memberRole})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		firstToken := tokenFromLink(t, sender.lastLink(t))
		resent, err := svc.Resend(ctx, userA, orgA, invitation.ID)
		if err != nil || resent.Status != invitations.StatusPending {
			t.Fatalf("resend: %+v, %v", resent, err)
		}
		secondToken := tokenFromLink(t, sender.lastLink(t))
		if firstToken == secondToken {
			t.Fatal("resending must issue a new token")
		}
		// Only the newest link works.
		_, err = svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: firstToken, Password: "StrongPassword123", FirstName: "A", LastName: "B",
		}, auth.RequestMeta{})
		requireAPIError(t, err, 404, "INVITATION_NOT_FOUND")

		revoked, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "revoke@example.com", RoleID: memberRole})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		revokeToken := tokenFromLink(t, sender.lastLink(t))
		if err := svc.Revoke(ctx, userA, orgA, revoked.ID); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		_, err = svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: revokeToken, Password: "StrongPassword123", FirstName: "A", LastName: "B",
		}, auth.RequestMeta{})
		requireAPIError(t, err, 410, "INVITATION_REVOKED")
		// Revoking twice, or resending a revoked invitation, is a safe 404/409.
		requireAPIError(t, svc.Revoke(ctx, userA, orgA, revoked.ID), 404, "INVITATION_NOT_FOUND")
		_, err = svc.Resend(ctx, userA, orgA, revoked.ID)
		requireAPIError(t, err, 409, "INVITATION_NOT_ACTIVE")
		// Accepting with the current link spends it, and a spent invitation can
		// neither be resent nor revoked.
		if _, err := svc.Accept(ctx, uuid.Nil, invitations.AcceptInput{
			Token: secondToken, Password: "StrongPassword123", FirstName: "Rota", LastName: "Tion",
		}, auth.RequestMeta{}); err != nil {
			t.Fatalf("accept the resent invitation: %v", err)
		}
		_, err = svc.Resend(ctx, userA, orgA, invitation.ID)
		requireAPIError(t, err, 409, "INVITATION_NOT_ACTIVE")
		requireAPIError(t, svc.Revoke(ctx, userA, orgA, invitation.ID), 404, "INVITATION_NOT_FOUND")
	})

	t.Run("role permissions are enforced", func(t *testing.T) {
		viewer := joinMember(t, pool, orgA, "inv-viewer@example.com", "Viewer")
		if _, _, err := svc.List(ctx, viewer, orgA, invitations.ListFilter{}, firstPage); err == nil {
			t.Fatal("a viewer must not read invitations")
		} else {
			requireAPIError(t, err, 403, "FORBIDDEN")
		}
		_, err := svc.Create(ctx, viewer, orgA, invitations.CreateInput{Email: "x@example.com", RoleID: memberRole})
		requireAPIError(t, err, 403, "FORBIDDEN")

		member := joinMember(t, pool, orgA, "inv-member@example.com", "Member")
		requireAPIError(t, func() error {
			_, err := svc.Create(ctx, member, orgA, invitations.CreateInput{Email: "y@example.com", RoleID: memberRole})
			return err
		}(), 403, "FORBIDDEN")

		// Only an Owner may invite another Owner.
		admin := joinMember(t, pool, orgA, "inv-admin@example.com", "Admin")
		ownerRole := roleIDOf(t, pool, orgA, "Owner")
		_, err = svc.Create(ctx, admin, orgA, invitations.CreateInput{Email: "new-owner@example.com", RoleID: ownerRole})
		requireAPIError(t, err, 403, "FORBIDDEN")
		invitedOwner, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "new-owner@example.com", RoleID: ownerRole})
		if err != nil {
			t.Fatalf("owner may invite an owner: %v", err)
		}
		// An Admin holds members.manage and may manage invitations, while a
		// Manager may not.
		manager := joinMember(t, pool, orgA, "inv-manager@example.com", "Manager")
		requireAPIError(t, svc.Revoke(ctx, manager, orgA, invitedOwner.ID), 403, "FORBIDDEN")
		if err := svc.Revoke(ctx, admin, orgA, invitedOwner.ID); err != nil {
			t.Fatalf("admin revoke: %v", err)
		}
	})

	t.Run("invitations never cross tenants", func(t *testing.T) {
		mine, pag, err := svc.List(ctx, userA, orgA, invitations.ListFilter{}, firstPage)
		if err != nil || pag.Total != int64(len(mine)) || pag.Total == 0 {
			t.Fatalf("unexpected list: %d, %v", pag, err)
		}
		// Org B sees none of org A's invitations.
		_, pag, err = svc.List(ctx, userB, orgB, invitations.ListFilter{}, firstPage)
		if err != nil || pag.Total != 0 {
			t.Fatalf("org B sees org A invitations: %+v, %v", pag, err)
		}
		target := mine[0]
		// Cross-tenant access to a specific invitation is a safe 404.
		requireAPIError(t, svc.Revoke(ctx, userB, orgB, target.ID), 404, "INVITATION_NOT_FOUND")
		_, err = svc.Resend(ctx, userB, orgB, target.ID)
		requireAPIError(t, err, 404, "INVITATION_NOT_FOUND")
		// Inviting into an organization the caller does not belong to is a 404.
		_, err = svc.Create(ctx, userB, orgA, invitations.CreateInput{Email: "x@example.com", RoleID: memberRole})
		requireAPIError(t, err, 404, "ORGANIZATION_NOT_FOUND")
		// Org A's invitation cannot be redeemed by someone who is not invited.
		other, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "inv-b@example.com", RoleID: memberRole})
		if err == nil {
			// userB is already the owner of org B, so inviting them to org A is allowed.
			if other.RoleName != "Member" {
				t.Fatalf("unexpected invitation: %+v", other)
			}
		} else {
			requireAPIError(t, err, 409, "")
		}
	})

	t.Run("list filtering and pagination", func(t *testing.T) {
		pending := invitations.StatusPending
		items, pag, err := svc.List(ctx, userA, orgA, invitations.ListFilter{Status: &pending}, firstPage)
		if err != nil || pag.Total == 0 || pag.Total != int64(len(items)) {
			t.Fatalf("status filter: %d, %+v, %v", pag.Total, items, err)
		}
		for _, inv := range items {
			if inv.Status != pending {
				t.Fatalf("status filter leaked %+v", inv)
			}
		}
		email := "rotate@example.com"
		items, pag, err = svc.List(ctx, userA, orgA, invitations.ListFilter{Email: &email}, firstPage)
		if err != nil || pag.Total != 1 || items[0].Email != email {
			t.Fatalf("email filter: %+v, %+v, %v", items, pag, err)
		}
		accepted := invitations.StatusAccepted
		items, pag, err = svc.List(ctx, userA, orgA, invitations.ListFilter{Status: &accepted}, firstPage)
		if err != nil || pag.Total == 0 {
			t.Fatalf("accepted filter: %d, %v", pag.Total, err)
		}
		for _, inv := range items {
			if inv.Status != accepted {
				t.Fatalf("status filter leaked %+v", inv)
			}
		}
		// Sorting by expiry ascending puts the soonest deadline first.
		items, _, err = svc.List(ctx, userA, orgA, invitations.ListFilter{Sort: "expires_at"}, firstPage)
		if err != nil || len(items) < 2 {
			t.Fatalf("sort: %+v, %v", items, err)
		}
		for i := 1; i < len(items); i++ {
			if items[i].ExpiresAt.Before(items[i-1].ExpiresAt) {
				t.Fatalf("expiry sort is not ascending: %+v", items)
			}
		}
		_, smallPage, err := svc.List(ctx, userA, orgA, invitations.ListFilter{}, httpx.PageRequest{Page: 1, PageSize: 2})
		if err != nil || smallPage.PageSize != 2 || smallPage.TotalPages < 2 {
			t.Fatalf("pagination metadata: %+v, %v", smallPage, err)
		}
	})

	t.Run("delivery failures do not lose the invitation", func(t *testing.T) {
		sender.err = errors.New("smtp unavailable")
		defer func() { sender.err = nil }()
		invitation, err := svc.Create(ctx, userA, orgA, invitations.CreateInput{Email: "unreachable@example.com", RoleID: memberRole})
		if err != nil {
			t.Fatalf("a delivery failure must not fail the request: %v", err)
		}
		if invitation.Status != invitations.StatusPending {
			t.Fatalf("unexpected invitation: %+v", invitation)
		}
		// The invitation still exists and is listed, so it can be resent.
		email := "unreachable@example.com"
		_, pag, err := svc.List(ctx, userA, orgA, invitations.ListFilter{Email: &email}, firstPage)
		if err != nil || pag.Total != 1 {
			t.Fatalf("invitation must survive a delivery failure: %+v, %v", pag, err)
		}
	})

	t.Run("database rejects cross-tenant roles and duplicates", func(t *testing.T) {
		var pgErr *pgconn.PgError
		_, err := pool.Exec(ctx, `
			INSERT INTO invitations (organization_id, email, role_id, token_hash, invited_by, expires_at)
			VALUES ($1, 'cross@example.com', $2, $3, $4, now() + interval '1 day')`,
			orgA, roleIDOf(t, pool, orgB, "Member"), "hash-cross-tenant", userA)
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "invitations_role_organization_fkey" {
			t.Fatalf("expected a cross-tenant role violation, got %v", err)
		}

		// Only one pending invitation per email, enforced by the database. A
		// settled invitation frees the address for a new one.
		if _, err := pool.Exec(ctx, `
			INSERT INTO invitations (organization_id, email, role_id, token_hash, invited_by, expires_at)
			VALUES ($1, 'reinvite@example.com', $2, 'hash-first', $3, now() + interval '1 day')`,
			orgA, memberRole, userA); err != nil {
			t.Fatalf("insert pending invitation: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO invitations (organization_id, email, role_id, token_hash, invited_by, expires_at)
			VALUES ($1, 'reinvite@example.com', $2, 'hash-duplicate', $3, now() + interval '1 day')`,
			orgA, memberRole, userA); !errors.As(err, &pgErr) || pgErr.Code != "23505" ||
			pgErr.ConstraintName != "idx_invitations_pending_email" {
			t.Fatalf("expected a duplicate pending invitation violation, got %v", err)
		}

		// An accepted invitation must record when and by whom it was accepted.
		if _, err := pool.Exec(ctx, `
			INSERT INTO invitations (organization_id, email, role_id, status, token_hash, invited_by, expires_at)
			VALUES ($1, 'inconsistent@example.com', $2, 'accepted', 'hash-inconsistent', $3, now() + interval '1 day')`,
			orgA, memberRole, userA); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected an accepted_at check violation, got %v", err)
		}
	})

	t.Run("RLS isolates invitations", func(t *testing.T) {
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
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM invitations WHERE organization_id = $1`, orgA).Scan(&visible); err != nil || visible != 0 {
			t.Fatalf("RLS leaked org A invitations into org B context: %d, %v", visible, err)
		}
	})
}

// roleIDOf returns the id of an organization role.
func roleIDOf(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	role, err := db.New(pool).GetRoleByName(context.Background(), db.GetRoleByNameParams{
		OrganizationID: orgID, Name: name,
	})
	if err != nil {
		t.Fatalf("get role %s: %v", name, err)
	}
	return role.ID
}
