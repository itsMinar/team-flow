-- name: ListMembersByOrganization :many
SELECT
    m.id,
    m.organization_id,
    m.user_id,
    m.role_id,
    m.status,
    m.joined_at,
    m.created_at,
    m.updated_at,
    u.email,
    u.first_name,
    u.last_name,
    r.name AS role_name
FROM organization_memberships m
JOIN users u ON u.id = m.user_id
JOIN roles r ON r.id = m.role_id
WHERE m.organization_id = $1
ORDER BY u.email;
