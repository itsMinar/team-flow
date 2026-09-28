-- name: CreateActivityLog :exec
INSERT INTO activity_logs (organization_id, actor_user_id, action, resource_type, resource_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListActivityByResource :many
SELECT * FROM activity_logs
WHERE organization_id = sqlc.arg('organization_id')
  AND resource_type = sqlc.arg('resource_type')
  AND resource_id = sqlc.arg('resource_id')
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountActivityByResource :one
SELECT count(*) FROM activity_logs
WHERE organization_id = sqlc.arg('organization_id')
  AND resource_type = sqlc.arg('resource_type')
  AND resource_id = sqlc.arg('resource_id');
