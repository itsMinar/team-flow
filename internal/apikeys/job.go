package apikeys

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/jobs"
)

// JobTypeExpireSweep revokes API keys that expired long enough ago to be
// unambiguously dead.
const JobTypeExpireSweep = "api_keys.expire_sweep"

// Sweep policy.
const (
	// expiryGracePeriod keeps a recently expired key in the listing as expired
	// rather than revoked, so an operator can still see what happened before it is
	// swept away.
	expiryGracePeriod = 30 * 24 * time.Hour
	// expirySweepBatchSize bounds one sweep run per organization.
	expirySweepBatchSize = 200
)

// ExpireSweepJobPayload carries the sweep's options.
type ExpireSweepJobPayload struct {
	OrganizationID uuid.UUID `json:"organization_id,omitempty"`
}

// ExpireSweepJobHandler revokes expired keys in every active organization.
//
// Revocation is not needed for security — an expired key is already rejected —
// so this is housekeeping that keeps active listings and the key table from
// accumulating dead credentials.
func (s *Service) ExpireSweepJobHandler(ctx context.Context, job jobs.Job) error {
	var payload ExpireSweepJobPayload
	if len(job.Payload) > 0 {
		if err := decodePayload(job.Payload, &payload); err != nil {
			return jobs.Permanent(fmt.Errorf("decode %s payload: %w", JobTypeExpireSweep, err))
		}
	}

	var revoked int
	err := s.orgs.ForEachActiveOrganization(ctx, payload.OrganizationID, func(orgID uuid.UUID) error {
		count, err := s.revokeExpired(ctx, orgID)
		if err != nil {
			return err
		}
		revoked += count
		return nil
	})
	if err != nil {
		return err
	}
	if revoked > 0 {
		s.logger.Info("revoked expired api keys", slog.Int("count", revoked))
	}
	return nil
}

// revokeExpired revokes one organization's long-expired keys and reports how many
// were affected.
func (s *Service) revokeExpired(ctx context.Context, orgID uuid.UUID) (int, error) {
	var revoked int
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		rows, err := q.ListExpiredAPIKeys(ctx, db.ListExpiredAPIKeysParams{
			OrganizationID: orgID,
			ExpiredBefore:  time.Now().Add(-expiryGracePeriod),
			RowLimit:       expirySweepBatchSize,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := q.RevokeAPIKeyByID(ctx, db.RevokeAPIKeyByIDParams{
				ID: row.ID, OrganizationID: orgID,
			}); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("revoke expired api keys: %w", err)
	}
	return revoked, nil
}

func decodePayload(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}
