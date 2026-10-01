-- name: CreateAuditLog :one
INSERT INTO audit_logs (
    organization_id, actor_user_id, action, outcome, target_type, target_id,
    ip_address, user_agent, request_id, trace_id, metadata
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListAuditLogs :many
-- Reads are always tenant-scoped; rows without an organization are excluded by the
-- RLS select policy and can never be reached through this query.
-- ip_address is selected as text: pgx cannot scan the inet type into a Go string,
-- and the text form is what the API exposes anyway. An absent address becomes an
-- empty string, which the client-facing entry omits.
SELECT
    id, organization_id, actor_user_id, action, outcome, target_type, target_id,
    CAST(COALESCE(host(ip_address), '') AS text) AS ip_address, user_agent, request_id, trace_id, metadata, created_at
FROM audit_logs
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action')::text)
  AND (sqlc.narg('outcome')::text IS NULL OR outcome = sqlc.narg('outcome')::text)
  AND (sqlc.narg('actor_user_id')::uuid IS NULL OR actor_user_id = sqlc.narg('actor_user_id')::uuid)
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since')::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountAuditLogs :one
SELECT count(*) FROM audit_logs
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('action')::text IS NULL OR action = sqlc.narg('action')::text)
  AND (sqlc.narg('outcome')::text IS NULL OR outcome = sqlc.narg('outcome')::text)
  AND (sqlc.narg('actor_user_id')::uuid IS NULL OR actor_user_id = sqlc.narg('actor_user_id')::uuid)
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since')::timestamptz);