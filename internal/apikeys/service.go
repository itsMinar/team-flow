package apikeys

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

	"github.com/itsMinar/team-flow/internal/authctx"
	"github.com/itsMinar/team-flow/internal/database"
	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/httpx"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/permissions"
)

const oneTimeWarning = "This key is shown once. Store it now; it cannot be retrieved again."

// Service owns API key use cases and authenticates presented keys.
type Service struct {
	pool       *pgxpool.Pool
	q          *db.Queries
	orgs       *organizations.Service
	logger     *slog.Logger
	defaultTTL time.Duration
	maxTTL     time.Duration
}

// NewService constructs the API keys Service.
func NewService(pool *pgxpool.Pool, orgs *organizations.Service,
	defaultTTL, maxTTL time.Duration, logger *slog.Logger,
) *Service {
	return &Service{
		pool: pool, q: db.New(pool), orgs: orgs, logger: logger,
		defaultTTL: defaultTTL, maxTTL: maxTTL,
	}
}

// withOrgTx runs fn inside a transaction whose RLS context is confined to orgID.
func (s *Service) withOrgTx(ctx context.Context, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	return database.WithTenantTx(ctx, s.pool, orgID, fn)
}

func keyNotFound(err error) error {
	return httpx.NewAPIError(404, "API_KEY_NOT_FOUND", "API key not found", err)
}

// invalidKey is deliberately uniform: a caller must not be able to tell an
// unknown key from a revoked or expired one.
func invalidKey(err error) error {
	return httpx.NewAPIError(401, "INVALID_API_KEY", "Invalid or expired API key", err)
}

// Create mints a key for the calling user in their organization. The secret is
// returned once and stored only as a hash.
func (s *Service) Create(ctx context.Context, userID, orgID uuid.UUID, in CreateInput) (CreatedResult, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.APIKeysManage); err != nil {
		return CreatedResult{}, err
	}
	if err := in.validate(); err != nil {
		return CreatedResult{}, err
	}

	ttl := s.defaultTTL
	if in.ExpiresInDays > 0 {
		ttl = time.Duration(in.ExpiresInDays) * 24 * time.Hour
		if ttl > s.maxTTL {
			return CreatedResult{}, httpx.NewValidationError(map[string]string{
				"expires_in_days": fmt.Sprintf("must not exceed %d days", int(s.maxTTL.Hours()/24)),
			})
		}
	}

	raw, prefix, lastFour, err := generateKey()
	if err != nil {
		return CreatedResult{}, err
	}
	now := time.Now()

	var key db.ApiKey
	err = s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var err error
		key, err = s.q.WithTx(tx).CreateAPIKey(ctx, db.CreateAPIKeyParams{
			OrganizationID: orgID, CreatedBy: userID, Name: strings.TrimSpace(in.Name),
			KeyPrefix: prefix, KeyLastFour: lastFour, KeyHash: hashKey(raw),
			ExpiresAt: now.Add(ttl),
		})
		// The composite foreign key guarantees the creator is a member of this
		// organization; a concurrent membership removal surfaces as 23503.
		if err != nil {
			return mapWriteError(err)
		}
		return nil
	})
	if err != nil {
		return CreatedResult{}, fmt.Errorf("create api key: %w", err)
	}

	s.logger.Info("api key created",
		slog.String("api_key_id", key.ID.String()),
		slog.String("organization_id", orgID.String()),
		slog.String("created_by", userID.String()),
	)
	return CreatedResult{
		APIKey:  toDTO(key, now),
		Key:     raw,
		Warning: oneTimeWarning,
	}, nil
}

// List returns the organization's keys, excluding revoked ones by default.
func (s *Service) List(ctx context.Context, userID, orgID uuid.UUID, filter ListFilter, page httpx.PageRequest) ([]APIKeyDTO, httpx.Pagination, error) {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.APIKeysManage); err != nil {
		return nil, httpx.Pagination{}, err
	}
	sortBy := filter.Sort
	if !sortableFields[sortBy] {
		sortBy = defaultSort
	}
	var rows []db.ApiKey
	var total int64
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		params := db.CountAPIKeysParams{OrganizationID: orgID, IncludeRevoked: filter.IncludeRevoked}
		var err error
		if total, err = q.CountAPIKeys(ctx, params); err != nil {
			return err
		}
		rows, err = q.ListAPIKeys(ctx, db.ListAPIKeysParams{
			OrganizationID: orgID, IncludeRevoked: filter.IncludeRevoked,
			SortBy: sortBy, SortDesc: filter.Desc, PageLimit: page.Limit(), PageOffset: page.Offset(),
		})
		return err
	})
	if err != nil {
		return nil, httpx.Pagination{}, fmt.Errorf("list api keys: %w", err)
	}
	now := time.Now()
	result := make([]APIKeyDTO, 0, len(rows))
	for _, row := range rows {
		result = append(result, toDTO(row, now))
	}
	return result, httpx.NewPagination(page, total), nil
}

// Revoke invalidates a key immediately. Revoking an already revoked key is
// reported as not found so the endpoint cannot be used to probe key history.
func (s *Service) Revoke(ctx context.Context, userID, orgID, keyID uuid.UUID) error {
	if _, err := s.orgs.Authorize(ctx, userID, orgID, permissions.APIKeysManage); err != nil {
		return err
	}
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		current, err := q.GetAPIKeyByIDForUpdate(ctx, db.GetAPIKeyByIDForUpdateParams{
			ID: keyID, OrganizationID: orgID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return keyNotFound(err)
		} else if err != nil {
			return err
		}
		if current.RevokedAt != nil {
			return keyNotFound(nil)
		}
		now := time.Now()
		if _, err := q.RevokeAPIKey(ctx, db.RevokeAPIKeyParams{
			ID: keyID, OrganizationID: orgID, RevokedAt: &now,
		}); err != nil {
			return err
		}
		return nil
	})
}

// Authenticate resolves a presented API key to the principal it authenticates.
//
// The key is looked up by hash without a tenant context, because the tenant is
// exactly what the key reveals. Everything that matters for access — an active
// user, an unrevoked key, a live expiry, and the membership and permissions of
// the creator in the key's organization — is then re-resolved from live state
// for each request.
func (s *Service) Authenticate(ctx context.Context, raw string) (authctx.Principal, error) {
	if !validKeyFormat(raw) {
		return authctx.Principal{}, invalidKey(nil)
	}
	key, err := s.q.GetAPIKeyByHash(ctx, hashKey(raw))
	if errors.Is(err, pgx.ErrNoRows) {
		return authctx.Principal{}, invalidKey(err)
	} else if err != nil {
		return authctx.Principal{}, fmt.Errorf("get api key: %w", err)
	}
	now := time.Now()
	if status(key, now) != StatusActive {
		return authctx.Principal{}, invalidKey(nil)
	}

	var user db.User
	err = s.withOrgTx(ctx, key.OrganizationID, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		user, err = q.GetUserByID(ctx, key.CreatedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidKey(err)
		} else if err != nil {
			return err
		}
		if user.Status != "active" {
			return invalidKey(nil)
		}
		return nil
	})
	if err != nil {
		return authctx.Principal{}, err
	}

	// Best-effort usage stamp; failures must never fail an authenticated request.
	if err := s.q.TouchAPIKey(ctx, key.ID); err != nil {
		s.logger.Warn("failed to record api key usage",
			slog.String("api_key_id", key.ID.String()), slog.Any("error", err))
	}

	return authctx.Principal{
		UserID:         user.ID,
		Method:         authctx.MethodAPIKey,
		APIKeyID:       key.ID,
		OrganizationID: key.OrganizationID,
	}, nil
}

// mapWriteError converts a concurrent membership removal into a safe error.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" &&
		pgErr.ConstraintName == "api_keys_creator_organization_fkey" {
		return httpx.NewAPIError(409, "NOT_A_MEMBER",
			"Your membership in this organization is no longer active", err)
	}
	return err
}
