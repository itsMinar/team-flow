-- name: CreateProject :one
INSERT INTO projects (
    organization_id, team_id, name, description, status, priority,
    start_date, due_date, created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects
WHERE id = $1 AND organization_id = $2;

-- name: GetProjectForUpdate :one
SELECT * FROM projects
WHERE id = $1 AND organization_id = $2
FOR UPDATE;

-- name: UpdateProject :one
UPDATE projects
SET team_id = $3,
    name = $4,
    description = $5,
    status = $6,
    priority = $7,
    start_date = $8,
    due_date = $9
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: DeleteProject :exec
DELETE FROM projects
WHERE id = $1 AND organization_id = $2;

-- name: ListProjects :many
-- Sort keys are fixed CASE branches, so client input never becomes SQL.
SELECT * FROM projects
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority')::text)
  AND (sqlc.narg('team_id')::uuid IS NULL OR team_id = sqlc.narg('team_id')::uuid)
ORDER BY
    CASE WHEN sqlc.arg('sort_by')::text = 'name' AND NOT sqlc.arg('sort_desc')::bool THEN name END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'name' AND sqlc.arg('sort_desc')::bool THEN name END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND NOT sqlc.arg('sort_desc')::bool THEN created_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND sqlc.arg('sort_desc')::bool THEN created_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'updated_at' AND NOT sqlc.arg('sort_desc')::bool THEN updated_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'updated_at' AND sqlc.arg('sort_desc')::bool THEN updated_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'due_date' AND NOT sqlc.arg('sort_desc')::bool THEN due_date END ASC NULLS LAST,
    CASE WHEN sqlc.arg('sort_by')::text = 'due_date' AND sqlc.arg('sort_desc')::bool THEN due_date END DESC NULLS LAST,
    CASE WHEN sqlc.arg('sort_by')::text = 'priority' AND NOT sqlc.arg('sort_desc')::bool
        THEN array_position(ARRAY['low', 'medium', 'high', 'urgent']::text[], priority) END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'priority' AND sqlc.arg('sort_desc')::bool
        THEN array_position(ARRAY['low', 'medium', 'high', 'urgent']::text[], priority) END DESC,
    id ASC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountProjects :one
SELECT count(*) FROM projects
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority')::text)
  AND (sqlc.narg('team_id')::uuid IS NULL OR team_id = sqlc.narg('team_id')::uuid);
