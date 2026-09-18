-- name: CreateMembership :one
INSERT INTO organization_memberships (organization_id, user_id, role_id, status, joined_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetMembership :one
SELECT * FROM organization_memberships
WHERE organization_id = $1 AND user_id = $2;

-- name: ListMembershipsByUser :many
SELECT
    m.*,
    o.name AS organization_name,
    o.slug AS organization_slug,
    o.status AS organization_status,
    r.name AS role_name
FROM organization_memberships m
JOIN organizations o ON o.id = m.organization_id
JOIN roles r ON r.id = m.role_id
WHERE m.user_id = $1 AND m.status = 'active'
ORDER BY o.name;

-- name: CountActiveOwners :one
SELECT count(*)
FROM organization_memberships m
JOIN roles r ON r.id = m.role_id
WHERE m.organization_id = $1
  AND m.status = 'active'
  AND r.name = 'Owner';
