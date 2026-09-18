package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DecodeJSON decodes the request body into dst, rejecting unknown fields and
// oversized or malformed bodies with a client-safe validation error. The
// caller is expected to have wrapped the body with http.MaxBytesReader.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return NewAPIError(http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
				"Request body is too large", err)
		case errors.Is(err, io.EOF):
			return NewAPIError(http.StatusBadRequest, "INVALID_JSON",
				"Request body must not be empty", err)
		default:
			return NewAPIError(http.StatusBadRequest, "INVALID_JSON",
				"Request body contains invalid JSON", err)
		}
	}

	// Reject trailing data after the first JSON value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NewAPIError(http.StatusBadRequest, "INVALID_JSON",
			"Request body must contain a single JSON object", fmt.Errorf("multiple json values"))
	}
	return nil
}
