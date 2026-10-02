// Package database manages the PostgreSQL connection pool used throughout the
// application. The pool is created once at startup and injected into
// repositories rather than accessed globally.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/config"
)

// Pool wraps a pgx connection pool. It is safe for concurrent use.
type Pool struct {
	*pgxpool.Pool
}

// New creates and verifies a PostgreSQL connection pool using the provided
// configuration. It returns an error if the pool cannot be established or the
// database is unreachable.
func New(ctx context.Context, cfg config.DatabaseConfig) (*Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// Health verifies the database is reachable. Used by the readiness probe.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return p.Ping(ctx)
}

func validateRLSRole(superuser, bypassRLS bool) error {
	if superuser || bypassRLS {
		return fmt.Errorf("application database role must not be superuser or have BYPASSRLS")
	}
	return nil
}

// CheckRLSRole ensures the connected role cannot bypass row-level security.
// Production processes must use a dedicated non-superuser application role.
func (p *Pool) CheckRLSRole(ctx context.Context) error {
	var superuser, bypassRLS bool
	if err := p.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&superuser, &bypassRLS); err != nil {
		return fmt.Errorf("inspect application database role: %w", err)
	}
	if err := validateRLSRole(superuser, bypassRLS); err != nil {
		return err
	}
	return nil
}

// Close releases all connections held by the pool.
func (p *Pool) Close() {
	p.Pool.Close()
}

// WithTenantTx runs fn in a transaction whose RLS context is confined to orgID.
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
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
