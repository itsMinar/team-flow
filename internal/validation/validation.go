// Package validation provides lightweight, dependency-free input validation.
// Validators accumulate field-level errors and produce a single client-safe
// validation error mapped to HTTP 400.
package validation

import (
	"net/mail"
	"strings"
	"unicode"

	"github.com/itsMinar/team-flow/internal/httpx"
)

// Validator accumulates field errors keyed by field name.
type Validator struct {
	errors map[string]string
}

// New returns an empty Validator.
func New() *Validator {
	return &Validator{errors: make(map[string]string)}
}

// Check records msg for field when ok is false.
func (v *Validator) Check(ok bool, field, msg string) {
	if !ok {
		if _, exists := v.errors[field]; !exists {
			v.errors[field] = msg
		}
	}
}

// Valid reports whether no errors have been recorded.
func (v *Validator) Valid() bool { return len(v.errors) == 0 }

// Err returns nil if valid, otherwise an APIError carrying the field errors.
func (v *Validator) Err() error {
	if v.Valid() {
		return nil
	}
	return httpx.NewValidationError(v.errors)
}

// Required checks that a trimmed string is non-empty.
func (v *Validator) Required(field, value string) {
	v.Check(strings.TrimSpace(value) != "", field, "is required")
}

// MaxLen checks that value does not exceed n characters.
func (v *Validator) MaxLen(field, value string, n int) {
	v.Check(len(value) <= n, field, "is too long")
}

// Email validates an RFC 5322 addr-spec and records an error if invalid.
func (v *Validator) Email(field, value string) {
	_, err := mail.ParseAddress(value)
	v.Check(err == nil, field, "must be a valid email address")
}

// Password enforces a minimum strength policy and bcrypt's 72-byte limit.
func (v *Validator) Password(field, value string) {
	if len(value) > 72 {
		v.Check(false, field, "must be at most 72 bytes")
		return
	}
	if len(value) < 8 {
		v.Check(false, field, "must be at least 8 characters")
		return
	}
	var hasLetter, hasDigit bool
	for _, r := range value {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	v.Check(hasLetter && hasDigit, field, "must contain both letters and numbers")
}

// NormalizeEmail lowercases and trims an email for consistent storage and
// lookup.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
