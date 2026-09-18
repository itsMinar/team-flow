// Package observability provides structured logging and (in later phases)
// metrics and tracing primitives shared across the API and worker processes.
package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// contextKey is a private type to avoid context key collisions.
type contextKey int

const (
	requestIDKey contextKey = iota
)

// NewLogger builds a structured JSON logger writing to stdout at the given
// level. All processes share this logger configuration so log output is
// uniform and machine-parseable.
func NewLogger(level string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ContextWithRequestID returns a copy of ctx carrying the request ID.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID stored in ctx, if any.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}

// LoggerWithRequestID returns a logger annotated with the request ID from ctx,
// enabling request-scoped correlation across log lines.
func LoggerWithRequestID(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id, ok := RequestIDFromContext(ctx); ok {
		return base.With(slog.String("request_id", id))
	}
	return base
}
