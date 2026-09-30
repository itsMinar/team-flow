-- Phase 8: organization invitations.
--
-- An invitation is a single-use, expiring credential that lets someone who is
-- not yet an organization member join. Only the SHA-256 hash of the token is
-- stored, exactly as with refresh tokens, so a database leak cannot be replayed
-- into an organization membership.
--
-- The role travels with the invitation, so acceptance can create the membership
-- with the intended role in one transaction. Organization membership is created
-- on acceptance rather than at invitation time: an invitation is a pending offer,
-- not a member, and pre-creating a membership would give a second, conflicting
-- source of truth for "who has been invited".
--
-- The roles table needs a composite unique key so the invited role can be
-- pinned to the invitation's organization at the database level, matching the
-- invariant already enforced for teams and projects.
CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_id_organization_id
    ON roles (id, organization_id);
ALTER TABLE roles ADD CONSTRAINT roles_id_organization_id_key
    UNIQUE USING INDEX idx_roles_id_organization_id;

CREATE TABLE invitations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    email           CITEXT NOT NULL,
    role_id         UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    -- Only the SHA-256 hex digest of the token is persisted.
    token_hash      TEXT NOT NULL,
    invited_by      UUID NOT NULL REFERENCES users (id),
    expires_at      TIMESTAMPTZ NOT NULL,
    accepted_at     TIMESTAMPTZ,
    accepted_by     UUID REFERENCES users (id) ON DELETE SET NULL,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, organization_id),
    CONSTRAINT invitations_expiry_after_creation CHECK (expires_at > created_at),
    CONSTRAINT invitations_accepted_consistency CHECK (
        (status = 'accepted') = (accepted_at IS NOT NULL)),
    CONSTRAINT invitations_revoked_consistency CHECK (
        (status = 'revoked') = (revoked_at IS NOT NULL)),
    -- An accepted invitation is never revoked, and only one invitation per
    -- email can be pending at a time, so a resend rotates the existing row
    -- instead of creating a second live link for the same address.
    CONSTRAINT invitations_role_organization_fkey FOREIGN KEY (role_id, organization_id)
        REFERENCES roles (id, organization_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_invitations_token_hash ON invitations (token_hash);
CREATE UNIQUE INDEX idx_invitations_pending_email
    ON invitations (organization_id, email) WHERE status = 'pending';
CREATE INDEX idx_invitations_org_created_at ON invitations (organization_id, created_at);
CREATE INDEX idx_invitations_org_status ON invitations (organization_id, status);
CREATE INDEX idx_invitations_email ON invitations (email);

CREATE TRIGGER trg_invitations_updated_at
    BEFORE UPDATE ON invitations
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
CREATE POLICY invitations_isolation ON invitations
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE invitations FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON invitations TO teamflow_app;