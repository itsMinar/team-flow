// Package httpx provides shared HTTP helpers: the standard response envelope,
// typed error mapping, and JSON encoding used across all handlers.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/itsMinar/team-flow/internal/observability"
)

// SuccessResponse is the standard envelope for successful responses.
type SuccessResponse struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}

// ErrorResponse is the standard envelope for error responses.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries client-safe error details.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Response is already partially written; nothing more we can do but log.
		slog.Error("failed to encode json response", slog.Any("error", err))
	}
}

// WriteData writes a successful data response.
func WriteData(w http.ResponseWriter, status int, data any) {
	WriteJSON(w, status, SuccessResponse{Data: data})
}

// WriteError maps err to a client-safe error response and writes it. Internal
// details are never leaked to the client; they are logged instead.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	requestID, _ := observability.RequestIDFromContext(r.Context())
	apiErr := FromError(err)

	// Log server-side errors with full detail; client errors at debug level.
	if apiErr.Status >= http.StatusInternalServerError {
		observability.LoggerWithRequestID(r.Context(), logger).Error(
			"request failed",
			slog.String("code", apiErr.Code),
			slog.Int("status", apiErr.Status),
			slog.Any("error", err),
		)
	}

	WriteJSON(w, apiErr.Status, ErrorResponse{
		Error: ErrorBody{
			Code:      apiErr.Code,
			Message:   apiErr.Message,
			RequestID: requestID,
		},
	})
}
