// Package fieldtypes holds the small request/response field types shared by
// feature packages. Keeping them here avoids duplicating calendar-date and
// partial-update semantics across resources.
package fieldtypes

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// DateLayout is the wire format for calendar dates (YYYY-MM-DD).
const DateLayout = "2006-01-02"

// Date is a calendar date encoded as YYYY-MM-DD and stored as a SQL DATE.
type Date struct{ time.Time }

func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.Format(DateLayout)) }

func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// Optional distinguishes an omitted JSON field (Set=false) from an explicit
// null (Set=true, Value=nil) in PATCH-style updates.
type Optional[T any] struct {
	Set   bool
	Value *T
}

func (o *Optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// DateFromTime converts a nullable database timestamp/date into a Date.
func DateFromTime(t *time.Time) *Date {
	if t == nil {
		return nil
	}
	return &Date{Time: *t}
}

// TimeFromDate converts a Date into the nullable database value.
func TimeFromDate(d *Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}

// EqualDate reports whether two dates denote the same calendar day.
func EqualDate(a, b Date) bool { return a.Equal(b.Time) }

// EqualPtr compares two optional values, treating both-nil as equal.
func EqualPtr[T any](a, b *T, eq func(T, T) bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return eq(*a, *b)
}

// NormalizeOptionalText trims an optional text value and clears it when blank,
// so an empty description is stored as NULL instead of an empty string.
func NormalizeOptionalText(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return s
}
