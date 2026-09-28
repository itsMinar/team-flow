package projects

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func date(t *testing.T, s string) *Date {
	t.Helper()
	parsed, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return &Date{Time: parsed}
}

func TestCreateInputDefaultsAndValidation(t *testing.T) {
	f := CreateInput{Name: "  Website Redesign  "}.fields()
	if f.Name != "Website Redesign" || f.Status != defaultStatus || f.Priority != defaultPriority {
		t.Fatalf("unexpected defaults: %+v", f)
	}
	if err := f.validate(); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}

	invalid := []CreateInput{
		{Name: "   "},
		{Name: strings.Repeat("a", maxNameLength+1)},
		{Name: "X", Status: "done"},
		{Name: "X", Priority: "critical"},
		{Name: "X", StartDate: date(t, "2026-10-10"), DueDate: date(t, "2026-10-01")},
	}
	for _, in := range invalid {
		if err := in.fields().validate(); err == nil {
			t.Fatalf("expected validation failure for %+v", in)
		}
	}
}

func TestBlankDescriptionIsCleared(t *testing.T) {
	blank := "   "
	if f := (CreateInput{Name: "X", Description: &blank}).fields(); f.Description != nil {
		t.Fatalf("blank description should normalize to nil, got %q", *f.Description)
	}
}

func TestUpdateInputDistinguishesOmittedFromNull(t *testing.T) {
	teamID := uuid.New()
	current := fields{Name: "P", Status: "planning", Priority: "medium", TeamID: &teamID, DueDate: date(t, "2026-12-01")}

	var omitted UpdateInput
	if err := json.Unmarshal([]byte(`{"name":"P2"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	next, changed := omitted.apply(current)
	if next.TeamID == nil || *next.TeamID != teamID || next.DueDate == nil {
		t.Fatal("omitted fields must be preserved")
	}
	if !slices.Equal(changed, []string{"name"}) {
		t.Fatalf("changed = %v", changed)
	}

	var cleared UpdateInput
	if err := json.Unmarshal([]byte(`{"team_id":null,"due_date":null,"status":"active"}`), &cleared); err != nil {
		t.Fatal(err)
	}
	next, changed = cleared.apply(current)
	if next.TeamID != nil || next.DueDate != nil || next.Status != "active" {
		t.Fatalf("explicit null must clear fields: %+v", next)
	}
	if !slices.Equal(changed, []string{"team_id", "status", "due_date"}) {
		t.Fatalf("changed = %v", changed)
	}
}

func TestUpdateInputNoOpReportsNoChanges(t *testing.T) {
	current := fields{Name: "P", Status: "active", Priority: "high", DueDate: date(t, "2026-12-01")}
	var in UpdateInput
	if err := json.Unmarshal([]byte(`{"name":"P","status":"active","due_date":"2026-12-01"}`), &in); err != nil {
		t.Fatal(err)
	}
	if _, changed := in.apply(current); len(changed) != 0 {
		t.Fatalf("expected no changes, got %v", changed)
	}
}

func TestDateJSON(t *testing.T) {
	var d Date
	if err := json.Unmarshal([]byte(`"2026-10-05"`), &d); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil || string(out) != `"2026-10-05"` {
		t.Fatalf("round trip = %s, %v", out, err)
	}
	for _, bad := range []string{`"2026-13-01"`, `"05/10/2026"`, `20261005`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Fatalf("expected %s to be rejected", bad)
		}
	}
}

func TestParseListFilter(t *testing.T) {
	teamID := uuid.New()
	filter, err := parseListFilter(url.Values{
		"status": {"active"}, "priority": {"high"}, "team_id": {teamID.String()},
		"sort": {"due_date"}, "order": {"asc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *filter.Status != "active" || *filter.Priority != "high" || *filter.TeamID != teamID ||
		filter.Sort != "due_date" || filter.Desc {
		t.Fatalf("unexpected filter: %+v", filter)
	}

	defaults, err := parseListFilter(url.Values{})
	if err != nil || defaults.Sort != defaultSort || !defaults.Desc || defaults.Status != nil {
		t.Fatalf("unexpected defaults: %+v, %v", defaults, err)
	}
}

func TestParseListFilterRejectsUnsafeInput(t *testing.T) {
	cases := []url.Values{
		{"status": {"active' OR 1=1 --"}},
		{"priority": {"extreme"}},
		{"team_id": {"not-a-uuid"}},
		{"sort": {"password_hash"}},
		{"sort": {"name; DROP TABLE projects"}},
		{"order": {"sideways"}},
	}
	for _, values := range cases {
		if _, err := parseListFilter(values); err == nil {
			t.Fatalf("expected %v to be rejected", values)
		}
	}
}
