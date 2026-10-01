// Package observability provides the structured logging, request correlation, and
// trace propagation primitives shared across the API and worker processes.
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
	traceIDKey
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

// LoggerWithRequestID returns a logger annotated with the request and trace IDs
// from ctx, enabling correlation across log lines. It is the helper every call
// site should use.
func LoggerWithRequestID(ctx context.Context, base *slog.Logger) *slog.Logger {
	return LoggerWithRequest(ctx, base)
}

// TraceIDFromContext returns the trace ID stored in ctx, if any.
func TraceIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(traceIDKey).(string)
	return id, ok
}

// ContextWithTraceID returns a copy of ctx carrying a trace ID.
func ContextWithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// LoggerWithRequest returns a logger annotated with the request and trace IDs from
// ctx, so one log line can be correlated with a request and with a distributed
// trace without the caller threading identifiers by hand.
func LoggerWithRequest(ctx context.Context, base *slog.Logger) *slog.Logger {
	logger := base
	if id, ok := RequestIDFromContext(ctx); ok {
		logger = logger.With(slog.String("request_id", id))
	}
	if id, ok := TraceIDFromContext(ctx); ok && id != "" {
		logger = logger.With(slog.String("trace_id", id))
	}
	return logger
}
