package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listMembersByOrganization = `-- name: ListMembersByOrganization :many
SELECT
    m.id, m.organization_id, m.user_id, m.role_id, m.status, m.joined_at, m.created_at, m.updated_at,
    u.email, u.first_name, u.last_name,
    r.name AS role_name
FROM organization_memberships m
JOIN users u ON u.id = m.user_id
JOIN roles r ON r.id = m.role_id
WHERE m.organization_id = $1
ORDER BY u.email
`

// ListMembersByOrganizationRow joins membership with user and role info.
type ListMembersByOrganizationRow struct {
	ID             uuid.UUID
	OrganizationID uuid.UUID
	UserID         uuid.UUID
	RoleID         uuid.UUID
	Status         string
	JoinedAt       *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Email          string
	FirstName      string
	LastName       string
	RoleName       string
}

// ListMembersByOrganization returns every member of an organization.
func (q *Queries) ListMembersByOrganization(ctx context.Context, organizationID uuid.UUID) ([]ListMembersByOrganizationRow, error) {
	rows, err := q.db.Query(ctx, listMembersByOrganization, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ListMembersByOrganizationRow{}
	for rows.Next() {
		var i ListMembersByOrganizationRow
		if err := rows.Scan(
			&i.ID, &i.OrganizationID, &i.UserID, &i.RoleID, &i.Status, &i.JoinedAt,
			&i.CreatedAt, &i.UpdatedAt, &i.Email, &i.FirstName, &i.LastName, &i.RoleName,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
