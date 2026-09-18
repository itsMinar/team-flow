package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/itsMinar/team-flow/internal/httpx"
)

func TestPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		valid    bool
	}{
		{name: "minimum length", password: "abcdefg1", valid: true},
		{name: "maximum bytes", password: strings.Repeat("a", 71) + "1", valid: true},
		{name: "too many bytes", password: strings.Repeat("a", 72) + "1"},
		{name: "unicode maximum bytes", password: strings.Repeat("\u00e9", 35) + "a1", valid: true},
		{name: "unicode too many bytes", password: strings.Repeat("\u00e9", 36) + "1"},
		{name: "empty"},
		{name: "too short", password: "abcdef1"},
		{name: "missing digit", password: "abcdefgh"},
		{name: "missing letter", password: "12345678"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := New()
			validator.Password("password", test.password)
			if validator.Valid() != test.valid {
				t.Fatalf("Valid() = %v, want %v", validator.Valid(), test.valid)
			}
			if !test.valid && !errors.Is(validator.Err(), httpx.ErrValidation) {
				t.Fatalf("got %v, want a validation error", validator.Err())
			}
		})
	}
}
