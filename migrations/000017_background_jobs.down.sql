DROP INDEX IF EXISTS idx_invitations_undelivered;

ALTER TABLE invitations DROP COLUMN IF EXISTS notified_at;
ALTER TABLE invitations DROP COLUMN IF EXISTS delivery_attempts;
