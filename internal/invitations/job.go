package invitations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/jobs"
)

// Job types handled by the worker.
const (
	// JobTypeEmail delivers one invitation email. The payload carries the raw
	// token because only its hash is stored; the queue encrypts payloads, so the
	// link never sits in Redis in the clear.
	JobTypeEmail = "invitation.email"
	// JobTypeRedeliver is the periodic sweep that queues invitations which were
	// never handed to the queue, for example because Redis was unavailable when
	// the invitation was created.
	JobTypeRedeliver = "invitations.redeliver"
)

// Redelivery policy for invitations that were never queued.
const (
	// redeliveryGracePeriod keeps a newly created invitation out of the sweep, so
	// the create-time enqueue has a chance to run first.
	redeliveryGracePeriod = 2 * time.Minute
	// redeliveryBatchSize bounds one sweep run.
	redeliveryBatchSize = 100
	// redeliveryMaxAttempts is how many times a sweep will rotate a token for an
	// invitation whose delivery keeps failing. After that the invitation stays
	// pending and can be resent by an administrator.
	redeliveryMaxAttempts = 3
)

// EmailJobPayload is the encrypted body of an invitation email job.
type EmailJobPayload struct {
	InvitationID uuid.UUID `json:"invitation_id"`
	Token        string    `json:"token"`
}

// RedeliverJobPayload carries the sweep's options. It is empty by default so the
// job can be scheduled without arguments.
type RedeliverJobPayload struct {
	OrganizationID uuid.UUID `json:"organization_id,omitempty"`
}

// EmailJobHandler delivers the invitation email for one invitation.
//
// It is deliberately safe to run more than once: a reclaimed or replayed job
// either sends a link that is still pending or notices that the invitation is no
// longer pending and stops.
func (s *Service) EmailJobHandler(ctx context.Context, job jobs.Job) error {
	var payload EmailJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		// A malformed payload will never become valid, so do not retry it.
		return jobs.Permanent(fmt.Errorf("decode %s payload: %w", JobTypeEmail, err))
	}
	if payload.InvitationID == uuid.Nil || payload.Token == "" {
		return jobs.Permanent(errors.New("invitation email job is missing its invitation or token"))
	}

	invitation, orgName, roleName, err := s.deliveryTarget(ctx, payload.InvitationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The organization or invitation is gone; there is nothing to deliver
			// and nothing that a retry would fix.
			s.logger.Warn("invitation email job skipped: invitation no longer exists",
				slog.String("invitation_id", payload.InvitationID.String()))
			return nil
		}
		return err
	}
	if invitation.Status != StatusPending {
		s.logger.Info("invitation email job skipped: invitation is no longer pending",
			slog.String("invitation_id", payload.InvitationID.String()),
			slog.String("status", invitation.Status),
		)
		return nil
	}

	s.sendInvitationEmail(ctx, invitation, toDTO(invitation, roleName, time.Now()), orgName, payload.Token)

	// Record delivery so the sweep stops considering this invitation.
	if err := s.markNotified(ctx, payload.InvitationID, invitation.OrganizationID); err != nil {
		// The email is on its way; failing to record it would make the sweep send
		// another copy, so this is logged rather than retried.
		s.logger.Error("failed to record invitation delivery",
			slog.String("invitation_id", payload.InvitationID.String()),
			slog.Any("error", err))
	}
	return nil
}

// RedeliverJobHandler queues invitation emails that were never queued.
//
// The previous link of such an invitation was never delivered, so rotating the
// token is safe, and it keeps the "only the hash is stored" property intact: the
// sweep never has to recover a token, it mints a new one.
func (s *Service) RedeliverJobHandler(ctx context.Context, job jobs.Job) error {
	var payload RedeliverJobPayload
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return jobs.Permanent(fmt.Errorf("decode %s payload: %w", JobTypeRedeliver, err))
		}
	}

	var queued int
	err := s.orgs.ForEachActiveOrganization(ctx, payload.OrganizationID, func(orgID uuid.UUID) error {
		rows, err := s.undeliveredInvitations(ctx, orgID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			token, err := generateToken()
			if err != nil {
				return err
			}
			var rotated db.Invitation
			err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
				q := s.q.WithTx(tx)
				updated, err := q.RotateInvitationForDelivery(ctx, db.RotateInvitationForDeliveryParams{
					ID: row.ID, OrganizationID: orgID, TokenHash: hashToken(token),
				})
				if errors.Is(err, pgx.ErrNoRows) {
					// Accepted, revoked, or already delivered in the meantime.
					return nil
				}
				if err != nil {
					return err
				}
				rotated = updated
				return nil
			})
			if err != nil {
				return err
			}
			if rotated.ID == uuid.Nil {
				continue
			}
			if _, err := s.enqueueEmail(ctx, rotated, token); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if queued > 0 {
		s.logger.Info("queued undelivered invitations", slog.Int("count", queued))
	}
	return nil
}

// enqueueEmail hands the invitation email to the worker. It reports the queue
// error so the caller can decide whether to fall back to inline delivery.
func (s *Service) enqueueEmail(ctx context.Context, invitation db.Invitation, token string) (jobs.Job, error) {
	if s.queue == nil {
		return jobs.Job{}, errors.New("jobs: no queue configured")
	}
	return s.queue.Enqueue(ctx, JobTypeEmail, EmailJobPayload{
		InvitationID: invitation.ID, Token: token,
	})
}

// deliveryTarget loads the invitation with the organization and role names needed
// to compose the email.
func (s *Service) deliveryTarget(ctx context.Context, invitationID uuid.UUID) (db.Invitation, string, string, error) {
	var (
		invitation db.Invitation
		orgName    string
		roleName   string
	)
	row, err := s.q.GetInvitationByIDForJob(ctx, invitationID)
	if err != nil {
		return invitation, orgName, roleName, err
	}
	err = s.withOrgTx(ctx, row.OrganizationID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		invitation, err = q.GetInvitationByID(ctx, db.GetInvitationByIDParams{
			ID: invitationID, OrganizationID: row.OrganizationID,
		})
		if err != nil {
			return err
		}
		orgName, err = s.orgName(ctx, q, invitation.OrganizationID)
		if err != nil {
			return err
		}
		role, err := q.GetRoleByID(ctx, db.GetRoleByIDParams{
			ID: invitation.RoleID, OrganizationID: invitation.OrganizationID,
		})
		if err != nil {
			return err
		}
		roleName = role.Name
		return nil
	})
	return invitation, orgName, roleName, err
}

// undeliveredInvitations lists pending invitations of one organization that were
// never queued for delivery.
func (s *Service) undeliveredInvitations(ctx context.Context, orgID uuid.UUID) ([]db.Invitation, error) {
	var rows []db.Invitation
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		rows, err = s.q.WithTx(tx).ListUndeliveredInvitations(ctx, db.ListUndeliveredInvitationsParams{
			OrganizationID:        orgID,
			MaxAttemptsComparison: 0,
			CreatedBefore:         time.Now().Add(-redeliveryGracePeriod),
			RowLimit:              redeliveryBatchSize,
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list undelivered invitations: %w", err)
	}
	return rows, nil
}

func (s *Service) markNotified(ctx context.Context, invitationID, orgID uuid.UUID) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return s.q.WithTx(tx).MarkInvitationNotified(ctx, db.MarkInvitationNotifiedParams{
			ID: invitationID, OrganizationID: orgID,
		})
	})
}
