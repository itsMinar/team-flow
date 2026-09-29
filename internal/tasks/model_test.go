package tasks

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/fieldtypes"
)

func date(t *testing.T, s string) *Date {
	t.Helper()
	parsed, err := time.Parse(fieldtypes.DateLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return &Date{Time: parsed}
}

func TestCreateInputDefaultsAndValidation(t *testing.T) {
	f := CreateInput{Title: "  Ship the billing page  "}.fields()
	if f.Title != "Ship the billing page" || f.Status != defaultStatus || f.Priority != defaultPriority {
		t.Fatalf("unexpected defaults: %+v", f)
	}
	if err := f.validate(); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}

	invalid := []CreateInput{
		{Title: "   "},
		{Title: strings.Repeat("a", maxTitleLength+1)},
		{Title: "X", Status: "done-ish"},
		{Title: "X", Priority: "critical"},
	}
	for _, in := range invalid {
		if err := in.fields().validate(); err == nil {
			t.Fatalf("expected validation failure for %+v", in)
		}
	}
}

func TestBlankDescriptionIsCleared(t *testing.T) {
	blank := "   "
	if f := (CreateInput{Title: "X", Description: &blank}).fields(); f.Description != nil {
		t.Fatalf("blank description should normalize to nil, got %q", *f.Description)
	}
}

func TestUpdateInputDistinguishesOmittedFromNull(t *testing.T) {
	assignee := uuid.New()
	current := fields{
		Title: "T", Status: "todo", Priority: "medium",
		AssigneeID: &assignee, DueDate: date(t, "2026-12-01"),
	}

	var omitted UpdateInput
	if err := json.Unmarshal([]byte(`{"title":"T2"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	next, changed := omitted.apply(current)
	if next.AssigneeID == nil || *next.AssigneeID != assignee || next.DueDate == nil {
		t.Fatal("omitted fields must be preserved")
	}
	if !slices.Equal(changed, []string{"title"}) {
		t.Fatalf("changed = %v", changed)
	}

	var cleared UpdateInput
	if err := json.Unmarshal([]byte(`{"assignee_id":null,"due_date":null,"status":"done"}`), &cleared); err != nil {
		t.Fatal(err)
	}
	next, changed = cleared.apply(current)
	if next.AssigneeID != nil || next.DueDate != nil || next.Status != "done" {
		t.Fatalf("explicit null must clear fields: %+v", next)
	}
	if !slices.Equal(changed, []string{"status", "assignee_id", "due_date"}) {
		t.Fatalf("changed = %v", changed)
	}
}

func TestUpdateInputNoOpReportsNoChanges(t *testing.T) {
	current := fields{Title: "T", Status: "in_progress", Priority: "high", DueDate: date(t, "2026-12-01")}
	var in UpdateInput
	if err := json.Unmarshal([]byte(`{"title":"T","status":"in_progress","due_date":"2026-12-01"}`), &in); err != nil {
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
	projectID, assigneeID := uuid.New(), uuid.New()
	filter, err := parseListFilter(url.Values{
		"project_id": {projectID.String()}, "status": {"in_progress"}, "priority": {"high"},
		"assignee_id": {assigneeID.String()}, "sort": {"due_date"}, "order": {"asc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *filter.ProjectID != projectID || *filter.Status != "in_progress" || *filter.Priority != "high" ||
		*filter.AssigneeID != assigneeID || filter.Sort != "due_date" || filter.Desc || filter.Unassigned {
		t.Fatalf("unexpected filter: %+v", filter)
	}

	unassigned, err := parseListFilter(url.Values{"unassigned": {"true"}})
	if err != nil || !unassigned.Unassigned || unassigned.AssigneeID != nil {
		t.Fatalf("unexpected unassigned filter: %+v, %v", unassigned, err)
	}

	defaults, err := parseListFilter(url.Values{})
	if err != nil || defaults.Sort != defaultSort || !defaults.Desc || defaults.Status != nil {
		t.Fatalf("unexpected defaults: %+v, %v", defaults, err)
	}
}

func TestParseListFilterRejectsUnsafeInput(t *testing.T) {
	cases := []url.Values{
		{"status": {"todo' OR 1=1 --"}},
		{"priority": {"extreme"}},
		{"project_id": {"not-a-uuid"}},
		{"assignee_id": {"not-a-uuid"}},
		{"unassigned": {"maybe"}},
		{"sort": {"password_hash"}},
		{"sort": {"title; DROP TABLE tasks"}},
		{"order": {"sideways"}},
		{"assignee_id": {uuid.NewString()}, "unassigned": {"true"}},
	}
	for _, values := range cases {
		if _, err := parseListFilter(values); err == nil {
			t.Fatalf("expected %v to be rejected", values)
		}
	}
}
