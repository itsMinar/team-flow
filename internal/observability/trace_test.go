package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraceMiddlewareAdoptsAValidInboundTrace(t *testing.T) {
	var captured string
	handler := TraceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = TraceIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(TraceparentHeader, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if captured != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q, want the inbound one", captured)
	}
	if got := rec.Header().Get(TraceIDHeader); got != captured {
		t.Fatalf("response trace header = %q", got)
	}
}

func TestTraceMiddlewareReplacesUnusableHeaders(t *testing.T) {
	cases := map[string]string{
		"absent":            "",
		"wrong version":     "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"short trace id":    "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
		"non hex trace id":  "00-zzzz92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"all zero trace id": "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"garbage":           "not-a-traceparent",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			var captured string
			handler := TraceMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = TraceIDFromContext(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if header != "" {
				req.Header.Set(TraceparentHeader, header)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if len(captured) != 32 || captured == "00000000000000000000000000000000" {
				t.Fatalf("trace id = %q, want a generated 32 character id", captured)
			}
		})
	}
}

func TestLoggerWithRequestCarriesRequestAndTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ctx := ContextWithRequestID(context.Background(), "req-123")
	ctx = ContextWithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	LoggerWithRequest(ctx, logger).Info("handled", slog.String("path", "/health"))

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not JSON: %s (%v)", buf.String(), err)
	}
	if record["request_id"] != "req-123" || record["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("unexpected log record: %v", record)
	}

	// A context without identifiers is still usable and adds no empty attributes.
	buf.Reset()
	record = nil
	LoggerWithRequest(context.Background(), logger).Info("plain")
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_id", "trace_id"} {
		if _, ok := record[key]; ok {
			t.Fatalf("a context without %s must not add the attribute: %v", key, record)
		}
	}
}

func TestLoggerWithRequestIDIsAnAlias(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ctx := ContextWithTraceID(context.Background(), "abc")
	LoggerWithRequestID(ctx, logger).Info("x")
	if !strings.Contains(buf.String(), `"trace_id":"abc"`) {
		t.Fatalf("existing call sites must pick up the trace id: %s", buf.String())
	}
}
