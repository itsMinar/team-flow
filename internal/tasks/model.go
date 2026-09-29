// Package tasks implements project-scoped tasks: assignment, statuses,
// priorities, due dates, filtering, and task activity.
package tasks

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
	"github.com/itsMinar/team-flow/internal/fieldtypes"
	"github.com/itsMinar/team-flow/internal/validation"
)

const (
	maxTitleLength       = 200
	maxDescriptionLength = 5000

	defaultStatus   = "todo"
	defaultPriority = "medium"
	defaultSort     = "created_at"
)

var (
	validStatuses = map[string]bool{
		"todo": true, "in_progress": true, "blocked": true,
		"in_review": true, "done": true, "cancelled": true,
	}
	validPriorities = map[string]bool{"low": true, "medium": true, "high": true, "urgent": true}
	sortableFields  = map[string]bool{
		"created_at": true, "updated_at": true, "title": true,
		"due_date": true, "priority": true, "status": true,
	}
)

// The messages below mirror the database CHECK constraints so invalid values
// are reported before a statement is sent.
const (
	statusList   = "must be one of todo, in_progress, blocked, in_review, done, cancelled"
	priorityList = "must be one of low, medium, high, urgent"
	sortableList = "must be one of created_at, updated_at, title, due_date, priority, status"
)

// Date is a calendar date encoded as YYYY-MM-DD.
type Date = fieldtypes.Date

// Optional distinguishes an omitted JSON field (Set=false) from an explicit null (Set=true, Value=nil).
type Optional[T any] = fieldtypes.Optional[T]

type CreateInput struct {
	Title       string     `json:"title"`
	Description *string    `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	AssigneeID  *uuid.UUID `json:"assignee_id"`
	DueDate     *Date      `json:"due_date"`
}

type UpdateInput struct {
	Title       *string             `json:"title"`
	Description Optional[string]    `json:"description"`
	Status      *string             `json:"status"`
	Priority    *string             `json:"priority"`
	AssigneeID  Optional[uuid.UUID] `json:"assignee_id"`
	DueDate     Optional[Date]      `json:"due_date"`
}

// ListFilter narrows a task collection. ProjectID is optional so the same
// filter serves both the organization-wide and per-project collections.
type ListFilter struct {
	ProjectID  *uuid.UUID
	Status     *string
	Priority   *string
	AssigneeID *uuid.UUID
	Unassigned bool
	Sort       string
	Desc       bool
}

type TaskDTO struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	ProjectID      uuid.UUID  `json:"project_id"`
	Title          string     `json:"title"`
	Description    *string    `json:"description"`
	Status         string     `json:"status"`
	Priority       string     `json:"priority"`
	AssigneeID     *uuid.UUID `json:"assignee_id"`
	DueDate        *Date      `json:"due_date"`
	CreatedBy      uuid.UUID  `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// fields is the mutable task state that create and update both validate.
type fields struct {
	Title       string
	Description *string
	Status      string
	Priority    string
	AssigneeID  *uuid.UUID
	DueDate     *Date
}

func (in CreateInput) fields() fields {
	f := fields{
		Title: strings.TrimSpace(in.Title), Description: fieldtypes.NormalizeOptionalText(in.Description),
		Status: in.Status, Priority: in.Priority, AssigneeID: in.AssigneeID, DueDate: in.DueDate,
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
	if in.Title != nil {
		next.Title = strings.TrimSpace(*in.Title)
		if next.Title != current.Title {
			changed = append(changed, "title")
		}
	}
	if in.Description.Set {
		next.Description = fieldtypes.NormalizeOptionalText(in.Description.Value)
		if !fieldtypes.EqualPtr(next.Description, current.Description, func(a, b string) bool { return a == b }) {
			changed = append(changed, "description")
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
	if in.AssigneeID.Set {
		next.AssigneeID = in.AssigneeID.Value
		if !fieldtypes.EqualPtr(next.AssigneeID, current.AssigneeID, func(a, b uuid.UUID) bool { return a == b }) {
			changed = append(changed, "assignee_id")
		}
	}
	if in.DueDate.Set {
		next.DueDate = in.DueDate.Value
		if !fieldtypes.EqualPtr(next.DueDate, current.DueDate, fieldtypes.EqualDate) {
			changed = append(changed, "due_date")
		}
	}
	return next, changed
}

func (f fields) validate() error {
	v := validation.New()
	v.Required("title", f.Title)
	v.MaxLen("title", f.Title, maxTitleLength)
	if f.Description != nil {
		v.MaxLen("description", *f.Description, maxDescriptionLength)
	}
	v.Check(validStatuses[f.Status], "status", statusList)
	v.Check(validPriorities[f.Priority], "priority", priorityList)
	return v.Err()
}

func fieldsFromTask(t db.Task) fields {
	return fields{
		Title: t.Title, Description: t.Description, Status: t.Status, Priority: t.Priority,
		AssigneeID: t.AssigneeID, DueDate: fieldtypes.DateFromTime(t.DueDate),
	}
}

func toDTO(t db.Task) TaskDTO {
	return TaskDTO{
		ID: t.ID, OrganizationID: t.OrganizationID, ProjectID: t.ProjectID,
		Title: t.Title, Description: t.Description, Status: t.Status, Priority: t.Priority,
		AssigneeID: t.AssigneeID, DueDate: fieldtypes.DateFromTime(t.DueDate),
		CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}
