// Package database manages the PostgreSQL connection pool used throughout the
// application. The pool is created once at startup and injected into
// repositories rather than accessed globally.
package database

import (
	"context"
	"fmt"
	"time"

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

// Close releases all connections held by the pool.
func (p *Pool) Close() {
	p.Pool.Close()
}
