package httpx

import (
	"errors"
	"net/http"
)

// Sentinel domain errors. Services return these (optionally wrapped) and the
// HTTP layer maps them to appropriate status codes and client-safe messages.
var (
	ErrNotFound      = errors.New("resource not found")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrForbidden     = errors.New("forbidden")
	ErrValidation    = errors.New("validation failed")
	ErrConflict      = errors.New("conflict")
	ErrUnprocessable = errors.New("unprocessable entity")
	ErrRateLimited   = errors.New("rate limited")
	ErrInternal      = errors.New("internal error")
)

// APIError is a typed error carrying an HTTP status, a stable machine-readable
// code, and a client-safe message.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Details carries optional field-level information (e.g. validation errors).
	// It must never contain sensitive or internal data.
	Details map[string]string
	// err is the wrapped underlying error, retained for logging but never
	// serialized to the client.
	err error
}

func (e *APIError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.Message
}

func (e *APIError) Unwrap() error { return e.err }

// NewAPIError builds an APIError with the given attributes.
func NewAPIError(status int, code, message string, cause error) *APIError {
	return &APIError{Status: status, Code: code, Message: message, err: cause}
}

// NewValidationError builds a 400 APIError carrying per-field messages.
func NewValidationError(fields map[string]string) *APIError {
	return &APIError{
		Status:  http.StatusBadRequest,
		Code:    "VALIDATION_ERROR",
		Message: "The request is invalid",
		Details: fields,
		err:     ErrValidation,
	}
}

// FromError converts an arbitrary error into an APIError. Known sentinel errors
// map to specific statuses; anything else is treated as an internal error so no
// implementation detail leaks to the client.
func FromError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return NewAPIError(http.StatusNotFound, "NOT_FOUND", "Resource not found", err)
	case errors.Is(err, ErrUnauthorized):
		return NewAPIError(http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required", err)
	case errors.Is(err, ErrForbidden):
		return NewAPIError(http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action", err)
	case errors.Is(err, ErrValidation):
		return NewAPIError(http.StatusBadRequest, "VALIDATION_ERROR", "The request is invalid", err)
	case errors.Is(err, ErrUnprocessable):
		return NewAPIError(http.StatusUnprocessableEntity, "UNPROCESSABLE_ENTITY", "The request could not be processed", err)
	case errors.Is(err, ErrConflict):
		return NewAPIError(http.StatusConflict, "CONFLICT", "The request conflicts with the current state", err)
	case errors.Is(err, ErrRateLimited):
		return NewAPIError(http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests", err)
	default:
		return NewAPIError(http.StatusInternalServerError, "INTERNAL_ERROR", "An internal error occurred", err)
	}
}
