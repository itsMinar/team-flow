// Package authctx carries the authenticated principal through the request
// context. It has no dependencies on other internal packages so both the
// authentication middleware (which sets the principal) and handlers (which read
// it) can depend on it without creating import cycles.
package authctx

import (
	"context"

	"github.com/google/uuid"
)

type contextKey int

const principalKey contextKey = iota

// Principal is the authenticated identity derived from a validated access
// token. It intentionally holds only identifiers; roles and permissions are
// resolved from the database when needed rather than trusted from the token.
type Principal struct {
	UserID  uuid.UUID
	TokenID uuid.UUID
}

// WithPrincipal returns a copy of ctx carrying the authenticated principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// PrincipalFromContext returns the authenticated principal stored in ctx.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
