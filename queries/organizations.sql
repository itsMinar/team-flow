-- name: CreateOrganization :one
INSERT INTO organizations (name, slug, status)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOrganizationByID :one
SELECT * FROM organizations
WHERE id = $1;

-- name: GetOrganizationBySlug :one
SELECT * FROM organizations
WHERE slug = $1;

-- name: OrganizationSlugExists :one
SELECT EXISTS (
    SELECT 1 FROM organizations WHERE slug = $1
);

-- name: ListActiveOrganizationIDs :many
-- Background maintenance jobs enumerate tenants and then run tenant-scoped work
-- inside each organization's own transaction.
SELECT id FROM organizations
WHERE status = 'active'
ORDER BY created_at ASC
LIMIT sqlc.arg('row_limit')::bigint;
