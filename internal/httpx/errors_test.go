package httpx

import (
	"fmt"
	"net/http"
	"testing"
)

func TestFromError_MapsSentinels(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"unauthorized", ErrUnauthorized, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"forbidden", ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
		{"validation", ErrValidation, http.StatusBadRequest, "VALIDATION_ERROR"},
		{"conflict", ErrConflict, http.StatusConflict, "CONFLICT"},
		{"rate limited", ErrRateLimited, http.StatusTooManyRequests, "RATE_LIMITED"},
		{"unknown", fmt.Errorf("some internal boom"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromError(tt.err)
			if got.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", got.Status, tt.wantStatus)
			}
			if got.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", got.Code, tt.wantCode)
			}
		})
	}
}

func TestFromError_WrappedSentinel(t *testing.T) {
	err := fmt.Errorf("loading project: %w", ErrNotFound)
	got := FromError(err)
	if got.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got.Status)
	}
}

func TestFromError_PreservesAPIError(t *testing.T) {
	orig := NewAPIError(http.StatusTeapot, "TEAPOT", "I'm a teapot", nil)
	got := FromError(orig)
	if got != orig {
		t.Error("expected FromError to return the same APIError instance")
	}
}
