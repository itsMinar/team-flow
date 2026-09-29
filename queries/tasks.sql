-- name: CreateTask :one
INSERT INTO tasks (
    organization_id, project_id, title, description, status, priority,
    assignee_id, due_date, created_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetTask :one
SELECT * FROM tasks
WHERE id = $1 AND organization_id = $2;

-- name: GetTaskForUpdate :one
SELECT * FROM tasks
WHERE id = $1 AND organization_id = $2
FOR UPDATE;

-- name: UpdateTask :one
UPDATE tasks
SET title = $3,
    description = $4,
    status = $5,
    priority = $6,
    assignee_id = $7,
    due_date = $8
WHERE id = $1 AND organization_id = $2
RETURNING *;

-- name: DeleteTask :exec
DELETE FROM tasks
WHERE id = $1 AND organization_id = $2;

-- name: ListTasks :many
-- Sort keys are fixed CASE branches, so client input never becomes SQL.
SELECT * FROM tasks
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority')::text)
  AND (
      (sqlc.narg('assignee_id')::uuid IS NOT NULL AND assignee_id = sqlc.narg('assignee_id')::uuid)
      OR (sqlc.narg('assignee_id')::uuid IS NULL AND sqlc.arg('unassigned')::bool AND assignee_id IS NULL)
      OR (sqlc.narg('assignee_id')::uuid IS NULL AND NOT sqlc.arg('unassigned')::bool)
  )
ORDER BY
    CASE WHEN sqlc.arg('sort_by')::text = 'title' AND NOT sqlc.arg('sort_desc')::bool THEN title END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'title' AND sqlc.arg('sort_desc')::bool THEN title END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND NOT sqlc.arg('sort_desc')::bool THEN created_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND sqlc.arg('sort_desc')::bool THEN created_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'updated_at' AND NOT sqlc.arg('sort_desc')::bool THEN updated_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'updated_at' AND sqlc.arg('sort_desc')::bool THEN updated_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'due_date' AND NOT sqlc.arg('sort_desc')::bool
        THEN due_date END ASC NULLS LAST,
    CASE WHEN sqlc.arg('sort_by')::text = 'due_date' AND sqlc.arg('sort_desc')::bool
        THEN due_date END DESC NULLS LAST,
    CASE WHEN sqlc.arg('sort_by')::text = 'priority' AND NOT sqlc.arg('sort_desc')::bool
        THEN array_position(ARRAY['low', 'medium', 'high', 'urgent']::text[], priority) END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'priority' AND sqlc.arg('sort_desc')::bool
        THEN array_position(ARRAY['low', 'medium', 'high', 'urgent']::text[], priority) END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'status' AND NOT sqlc.arg('sort_desc')::bool
        THEN array_position(
            ARRAY['todo', 'in_progress', 'blocked', 'in_review', 'done', 'cancelled']::text[], status) END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'status' AND sqlc.arg('sort_desc')::bool
        THEN array_position(
            ARRAY['todo', 'in_progress', 'blocked', 'in_review', 'done', 'cancelled']::text[], status) END DESC,
    id ASC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountTasks :one
SELECT count(*) FROM tasks
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('priority')::text IS NULL OR priority = sqlc.narg('priority')::text)
  AND (
      (sqlc.narg('assignee_id')::uuid IS NOT NULL AND assignee_id = sqlc.narg('assignee_id')::uuid)
      OR (sqlc.narg('assignee_id')::uuid IS NULL AND sqlc.arg('unassigned')::bool AND assignee_id IS NULL)
      OR (sqlc.narg('assignee_id')::uuid IS NULL AND NOT sqlc.arg('unassigned')::bool)
  );
