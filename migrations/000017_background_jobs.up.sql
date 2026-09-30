-- Phase 10: background jobs.
--
-- Invitation emails are delivered by the worker rather than in the request, so
-- an invitation must record that delivery was attempted. notified_at is the
-- proof of delivery; delivery_attempts bounds how often the redelivery sweep
-- rotates a token for a message that never reached a transport.
--
-- The token itself is never stored: the job payload carries it, encrypted, and
-- the sweep rotates the stored hash when it has to try again.
ALTER TABLE invitations ADD COLUMN notified_at TIMESTAMPTZ;
ALTER TABLE invitations ADD COLUMN delivery_attempts INT NOT NULL DEFAULT 0;

-- The redelivery sweep scans pending invitations that were never handed to the
-- queue, for example because Redis was unavailable at creation time.
CREATE INDEX idx_invitations_undelivered
    ON invitations (created_at) WHERE status = 'pending' AND notified_at IS NULL;
