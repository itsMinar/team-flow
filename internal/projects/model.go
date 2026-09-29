package projects

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/fieldtypes"
	"github.com/itsMinar/team-flow/internal/validation"
)

const (
	maxNameLength        = 150
	maxDescriptionLength = 5000

	defaultStatus   = "planning"
	defaultPriority = "medium"
	defaultSort     = "created_at"

	dateLayout = fieldtypes.DateLayout
)

var (
	validStatuses   = map[string]bool{"planning": true, "active": true, "on_hold": true, "completed": true, "archived": true}
	validPriorities = map[string]bool{"low": true, "medium": true, "high": true, "urgent": true}
	sortableFields  = map[string]bool{"created_at": true, "updated_at": true, "name": true, "due_date": true, "priority": true}
)

// Date is a calendar date encoded as YYYY-MM-DD.
type Date = fieldtypes.Date

// Optional distinguishes an omitted JSON field (Set=false) from an explicit null (Set=true, Value=nil).
type Optional[T any] = fieldtypes.Optional[T]

type CreateInput struct {
	Name        string     `json:"name"`
	Description *string    `json:"description"`
	TeamID      *uuid.UUID `json:"team_id"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	StartDate   *Date      `json:"start_date"`
	DueDate     *Date      `json:"due_date"`
}

type UpdateInput struct {
	Name        *string             `json:"name"`
	Description Optional[string]    `json:"description"`
	TeamID      Optional[uuid.UUID] `json:"team_id"`
	Status      *string             `json:"status"`
	Priority    *string             `json:"priority"`
	StartDate   Optional[Date]      `json:"start_date"`
	DueDate     Optional[Date]      `json:"due_date"`
}

type ListFilter struct {
	Status   *string
	Priority *string
	TeamID   *uuid.UUID
	Sort     string
	Desc     bool
}

type ProjectDTO struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	TeamID         *uuid.UUID `json:"team_id"`
	Name           string     `json:"name"`
	Description    *string    `json:"description"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	StartDate      *Date      `json:"start_date"`
	DueDate        *Date      `json:"due_date"`
	CreatedBy      uuid.UUID  `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// fields is the mutable project state that create and update both validate.
type fields struct {
	Name        string
	Description *string
	TeamID      *uuid.UUID
	Status      string
	Priority    string
	StartDate   *Date
	DueDate     *Date
}

func (in CreateInput) fields() fields {
	f := fields{
		Name: strings.TrimSpace(in.Name), Description: normalizeDescription(in.Description),
		TeamID: in.TeamID, Status: in.Status, Priority: in.Priority,
		StartDate: in.StartDate, DueDate: in.DueDate,
	}
	if f.Status == "" {
		f.Status = defaultStatus
	}
	if f.Priority == "" {
		f.Priority = defaultPriority
	}
	return f
}

// apply merges present fields onto current and reports which fields changed.
func (in UpdateInput) apply(current fields) (fields, []string) {
	next := current
	var changed []string
	if in.Name != nil {
		next.Name = strings.TrimSpace(*in.Name)
		if next.Name != current.Name {
			changed = append(changed, "name")
		}
	}
	if in.Description.Set {
		next.Description = normalizeDescription(in.Description.Value)
		if !equalPtr(next.Description, current.Description, func(a, b string) bool { return a == b }) {
			changed = append(changed, "description")
		}
	}
	if in.TeamID.Set {
		next.TeamID = in.TeamID.Value
		if !equalPtr(next.TeamID, current.TeamID, func(a, b uuid.UUID) bool { return a == b }) {
			changed = append(changed, "team_id")
		}
	}
	if in.Status != nil {
		next.Status = *in.Status
		if next.Status != current.Status {
			changed = append(changed, "status")
		}
	}
	if in.Priority != nil {
		next.Priority = *in.Priority
		if next.Priority != current.Priority {
			changed = append(changed, "priority")
		}
	}
	if in.StartDate.Set {
		next.StartDate = in.StartDate.Value
		if !equalPtr(next.StartDate, current.StartDate, equalDate) {
			changed = append(changed, "start_date")
		}
	}
	if in.DueDate.Set {
		next.DueDate = in.DueDate.Value
		if !equalPtr(next.DueDate, current.DueDate, equalDate) {
			changed = append(changed, "due_date")
		}
	}
	return next, changed
}

func (f fields) validate() error {
	v := validation.New()
	v.Required("name", f.Name)
	v.MaxLen("name", f.Name, maxNameLength)
	if f.Description != nil {
		v.MaxLen("description", *f.Description, maxDescriptionLength)
	}
	v.Check(validStatuses[f.Status], "status", "must be one of planning, active, on_hold, completed, archived")
	v.Check(validPriorities[f.Priority], "priority", "must be one of low, medium, high, urgent")
	if f.StartDate != nil && f.DueDate != nil {
		v.Check(!f.DueDate.Before(f.StartDate.Time), "due_date", "must not be before start_date")
	}
	return v.Err()
}

func fieldsFromProject(p db.Project) fields {
	return fields{
		Name: p.Name, Description: p.Description, TeamID: p.TeamID,
		Status: p.Status, Priority: p.Priority,
		StartDate: dateFromTime(p.StartDate), DueDate: dateFromTime(p.DueDate),
	}
}

func toDTO(p db.Project) ProjectDTO {
	return ProjectDTO{
		ID: p.ID, OrganizationID: p.OrganizationID, TeamID: p.TeamID,
		Name: p.Name, Description: p.Description, Status: p.Status, Priority: p.Priority,
		StartDate: dateFromTime(p.StartDate), DueDate: dateFromTime(p.DueDate),
		CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func normalizeDescription(s *string) *string {
	return fieldtypes.NormalizeOptionalText(s)
}

func dateFromTime(t *time.Time) *Date {
	return fieldtypes.DateFromTime(t)
}

func timeFromDate(d *Date) *time.Time {
	return fieldtypes.TimeFromDate(d)
}

func equalDate(a, b Date) bool { return fieldtypes.EqualDate(a, b) }

func equalPtr[T any](a, b *T, eq func(T, T) bool) bool {
	return fieldtypes.EqualPtr(a, b, eq)
}
