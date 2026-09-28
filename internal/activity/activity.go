// Package activity records user-facing, tenant-scoped activity events. These
// are distinct from security-oriented audit logs.
package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/itsMinar/team-flow/internal/db"
)

const (
	ResourceProject = "project"

	ProjectCreated = "project.created"
	ProjectUpdated = "project.updated"
	ProjectDeleted = "project.deleted"
)

type Entry struct {
	OrganizationID uuid.UUID
	ActorUserID    *uuid.UUID
	Action         string
	ResourceType   string
	ResourceID     uuid.UUID
	Metadata       map[string]any
}

type DTO struct {
	ID           uuid.UUID       `json:"id"`
	ActorUserID  *uuid.UUID      `json:"actor_user_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   uuid.UUID       `json:"resource_id"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

// Record must run on the caller's tenant transaction so the event commits or
// rolls back together with the change it describes.
func Record(ctx context.Context, q *db.Queries, e Entry) error {
	metadata := e.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode activity metadata: %w", err)
	}
	if err := q.CreateActivityLog(ctx, db.CreateActivityLogParams{
		OrganizationID: e.OrganizationID,
		ActorUserID:    e.ActorUserID,
		Action:         e.Action,
		ResourceType:   e.ResourceType,
		ResourceID:     e.ResourceID,
		Metadata:       raw,
	}); err != nil {
		return fmt.Errorf("record activity %s: %w", e.Action, err)
	}
	return nil
}

func ToDTO(l db.ActivityLog) DTO {
	return DTO{
		ID: l.ID, ActorUserID: l.ActorUserID, Action: l.Action,
		ResourceType: l.ResourceType, ResourceID: l.ResourceID,
		Metadata: json.RawMessage(l.Metadata), CreatedAt: l.CreatedAt,
	}
}
