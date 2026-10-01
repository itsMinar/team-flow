package observability

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// Trace propagation.
//
// This is deliberately lightweight: a W3C trace context is parsed or created,
// carried on the request, echoed to the caller, and attached to every log line.
// No exporter is registered, so nothing is sampled or shipped anywhere by
// default — which is the right behaviour for a service whose operator has not
// asked for tracing. An OpenTelemetry exporter can be added later without
// changing anything here, because the identifiers already exist.

const (
	// TraceparentHeader is the W3C trace context header.
	TraceparentHeader = "traceparent"
	// TraceIDHeader echoes the trace ID so a client can quote it in a support
	// request without parsing W3C headers.
	TraceIDHeader = "X-Trace-Id"
	// traceIDBytes is the W3C trace-id width.
	traceIDBytes = 16
	zeroTraceID  = "00000000000000000000000000000000"
)

// TraceMiddleware assigns or adopts a trace ID for the request.
//
// An inbound traceparent is reused when it is well formed and its trace ID is not
// all zeros, so a trace started by an upstream service keeps its identity. The
// header is also echoed on the response, which is what makes a single request
// traceable from the client's side.
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := traceIDFromHeader(r.Header.Get(TraceparentHeader))
		ctx := ContextWithTraceID(r.Context(), traceID)
		w.Header().Set(TraceIDHeader, traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// traceIDFromHeader extracts a valid trace ID from a traceparent value, or returns
// a fresh one. A malformed or zero trace ID is replaced rather than trusted.
func traceIDFromHeader(header string) string {
	fields := strings.Split(strings.TrimSpace(header), "-")
	if len(fields) == 4 &&
		fields[0] == "00" &&
		len(fields[1]) == traceIDBytes*2 &&
		isHex(fields[1]) &&
		fields[1] != zeroTraceID {
		return strings.ToLower(fields[1])
	}
	return newTraceID()
}

func newTraceID() string {
	buf := make([]byte, traceIDBytes)
	if _, err := rand.Read(buf); err != nil {
		// A weak identifier is still more useful than none: it keeps the log lines
		// of one request correlated with each other.
		return zeroTraceID
	}
	return hex.EncodeToString(buf)
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
