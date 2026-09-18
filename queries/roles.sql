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
