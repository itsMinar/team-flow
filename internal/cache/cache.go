// Package cache manages the Redis client used for caching, rate limiting, and
// background job coordination. The client is created once at startup and
// injected where needed.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/itsMinar/team-flow/internal/config"
)

// Client wraps a Redis client. It is safe for concurrent use.
type Client struct {
	*redis.Client
}

// New creates and verifies a Redis client using the provided configuration.
func New(ctx context.Context, cfg config.RedisConfig) (*Client, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	rdb := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &Client{Client: rdb}, nil
}

// Health verifies Redis is reachable. Used by the readiness probe.
func (c *Client) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.Ping(ctx).Err()
}

// Close closes the underlying Redis client.
func (c *Client) Close() error {
	return c.Client.Close()
}
