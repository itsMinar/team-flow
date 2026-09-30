-- name: CreateInvitation :one
INSERT INTO invitations (
    organization_id, email, role_id, status, token_hash, invited_by, expires_at
)
VALUES ($1, $2, $3, 'pending', $4, $5, $6)
RETURNING *;

-- name: GetInvitationByTokenHash :one
SELECT * FROM invitations
WHERE token_hash = $1;

-- name: GetInvitationByTokenHashForUpdate :one
SELECT * FROM invitations
WHERE token_hash = $1
FOR UPDATE;

-- name: GetInvitationByID :one
SELECT * FROM invitations
WHERE id = $1 AND organization_id = $2;

-- name: GetInvitationByIDForUpdate :one
SELECT * FROM invitations
WHERE id = $1 AND organization_id = $2
FOR UPDATE;

-- name: RotateInvitationToken :one
-- Resending reuses the pending invitation and issues a fresh token, so the
-- previous link stops working immediately.
UPDATE invitations
SET token_hash = sqlc.arg('token_hash'),
    expires_at = sqlc.arg('expires_at')
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id') AND status = 'pending'
RETURNING *;

-- name: AcceptInvitation :one
UPDATE invitations
SET status = 'accepted',
    accepted_at = sqlc.arg('accepted_at'),
    accepted_by = sqlc.arg('accepted_by')
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id')
RETURNING *;

-- name: RevokeInvitation :one
UPDATE invitations
SET status = 'revoked',
    revoked_at = sqlc.arg('revoked_at')
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id')
RETURNING *;

-- name: MarkInvitationExpired :one
UPDATE invitations
SET status = 'expired'
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id') AND status = 'pending'
RETURNING *;

-- name: ListInvitations :many
-- Sort keys are fixed CASE branches, so client input never becomes SQL.
SELECT * FROM invitations
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('email')::citext IS NULL OR email = sqlc.narg('email')::citext)
ORDER BY
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND NOT sqlc.arg('sort_desc')::bool THEN created_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'created_at' AND sqlc.arg('sort_desc')::bool THEN created_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'expires_at' AND NOT sqlc.arg('sort_desc')::bool THEN expires_at END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'expires_at' AND sqlc.arg('sort_desc')::bool THEN expires_at END DESC,
    CASE WHEN sqlc.arg('sort_by')::text = 'email' AND NOT sqlc.arg('sort_desc')::bool THEN email END ASC,
    CASE WHEN sqlc.arg('sort_by')::text = 'email' AND sqlc.arg('sort_desc')::bool THEN email END DESC,
    id ASC
LIMIT sqlc.arg('page_limit')::bigint OFFSET sqlc.arg('page_offset')::bigint;

-- name: CountInvitations :one
SELECT count(*) FROM invitations
WHERE organization_id = sqlc.arg('organization_id')
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status')::text)
  AND (sqlc.narg('email')::citext IS NULL OR email = sqlc.narg('email')::citext);

-- name: GetActiveMembershipByEmail :one
-- Used to reject inviting someone who is already part of the organization.
SELECT m.*
FROM organization_memberships m
JOIN users u ON u.id = m.user_id
WHERE m.organization_id = sqlc.arg('organization_id')
  AND u.email = sqlc.arg('email')
  AND m.status = 'active';
-- name: MarkInvitationNotified :exec
-- Records that the invitation email reached a transport.
UPDATE invitations
SET notified_at = now()
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id');

-- name: RotateInvitationForDelivery :one
-- The redelivery sweep issues a fresh token for an invitation that was never
-- queued. Rotating invalidates the previous link, which is safe because it was
-- never delivered, and increments the attempt counter so a permanently failing
-- transport is not retried forever.
UPDATE invitations
SET token_hash = sqlc.arg('token_hash'),
    delivery_attempts = delivery_attempts + 1
WHERE id = sqlc.arg('id') AND organization_id = sqlc.arg('organization_id')
  AND status = 'pending' AND notified_at IS NULL
RETURNING *;

-- name: ListUndeliveredInvitations :many
SELECT * FROM invitations
WHERE status = 'pending'
  AND notified_at IS NULL
  AND delivery_attempts = sqlc.arg('max_attempts_comparison')::int
  AND created_at < sqlc.arg('created_before')
  AND organization_id = sqlc.arg('organization_id')
ORDER BY created_at ASC
LIMIT sqlc.arg('row_limit')::bigint;

-- name: GetInvitationByIDForJob :one
-- Resolves an invitation's organization from an id that came from the job queue
-- rather than from a request, so the worker can open a tenant transaction for
-- it. It is never used to serve client input.
SELECT id, organization_id FROM invitations
WHERE id = sqlc.arg('id');
