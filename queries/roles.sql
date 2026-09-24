-- name: CreateRole :one
INSERT INTO roles (organization_id, name, description, is_system)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRoleByID :one
SELECT * FROM roles
WHERE id = $1 AND organization_id = $2;

-- name: GetRoleByName :one
SELECT * FROM roles
WHERE organization_id = $1 AND name = $2;

-- name: ListRolesByOrganization :many
SELECT * FROM roles
WHERE organization_id = $1
ORDER BY name;

-- name: UpdateRole :one
UPDATE roles
SET name = $3, description = $4
WHERE id = $1 AND organization_id = $2 AND is_system = false
RETURNING *;

-- name: DeleteRole :exec
DELETE FROM roles
WHERE id = $1 AND organization_id = $2 AND is_system = false;

-- name: CountMembershipsByRole :one
SELECT count(*)
FROM organization_memberships
WHERE role_id = $1 AND organization_id = $2;

-- name: ListPermissionKeysByRole :many
SELECT p.key
FROM permissions p
JOIN role_permissions rp ON rp.permission_id = p.id
WHERE rp.role_id = $1
ORDER BY p.key;

-- name: HasRolePermission :one
SELECT EXISTS (
	SELECT 1
	FROM role_permissions rp
	JOIN permissions p ON p.id = rp.permission_id
	WHERE rp.role_id = $1 AND p.key = $2
);

-- name: GetPermissionByKey :one
SELECT * FROM permissions
WHERE key = $1;

-- name: SetRolePermissions :exec
DELETE FROM role_permissions
WHERE role_id = $1;

-- name: AddRolePermission :exec
INSERT INTO role_permissions (role_id, permission_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;
