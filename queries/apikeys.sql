-- name: CreateAPIKey :one
INSERT INTO api_keys (
    organization_id, created_by, name, key_prefix, key_last_four, key_hash, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAPIKeyByHash :one
-- Authentication resolves a key by hash before a tenant is known, so this lookup
-- runs without an RLS tenant context, exactly like the login lookup by email.
SELECT * FROM api_keys
WHERE key_hash = $1;

-- name: GetAPIKeyByID :one
SELECT * FROM api_keys
WHERE id = $1 AND organization_id = $2;

-- name: GetAPIKeyByIDForUpdate :one
SELECT * FROM api_keys
WHERE id = $1 AND organization_id = $2
FOR UPDATE;

-- name: RevokeAPIKey :one
UPDATE api_keys
SET revoked_at = sqlc.arg('revoked_at')
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id')
  AND revoked_at IS NULL
RETURNING *;

-- name: TouchAPIKey :exec
-- Throttled to one write per minute per key so recording usage does not double
-- the write load of every authenticated request.
UPDATE api_keys
SET last_used_at = now()
WHERE id = $1
  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ListAPIKeys :many
-- Sort keys are fixed CASE branches, so client input never becomes SQL.
SELECT * FROM api_keys
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.arg('include_revoked')::bool OR revoked_at IS NULL)
ORDER BY
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND NOT sqlc.arg('sort_desc')::bool THEN created_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND sqlc.arg('sort_desc')::bool THEN created_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'expires_at' AND NOT sqlc.arg('sort_desc')::bool THEN expires_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'expires_at' AND sqlc.arg('sort_desc')::bool THEN expires_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'last_used_at' AND NOT sqlc.arg('sort_desc')::bool THEN last_used_at END ASC NULLS LAST,
    CASE WHEN sqlc.arg('sort_by')::text = 'last_used_at' AND sqlc.arg('sort_desc')::bool THEN last_used_at END DESC NULLS LAST,
    name ASC,
    id ASC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountAPIKeys :one
SELECT count(*) FROM api_keys
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.arg('include_revoked')::bool OR revoked_at IS NULL);
-- name: ListExpiredAPIKeys :many
-- Expired keys that have been dead long enough to be worth revoking. The grace
-- period keeps recently expired keys visible for an operator to review.
SELECT * FROM api_keys
WHERE organization_id = sqlc.arg('organization_id')
  AND revoked_at IS NULL
  AND expires_at < sqlc.arg('expired_before')
ORDER BY expires_at ASC
LIMIT sqlc.arg('row_limit')::bigint;

-- name: RevokeAPIKeyByID :exec
UPDATE api_keys
SET revoked_at = now()
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id');
