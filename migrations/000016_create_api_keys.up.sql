-- Phase 9: organization API keys.
--
-- An API key is an alternative credential for one (user, organization) pair. It
-- authenticates requests as the user who minted it, scoped to the organization
-- the key was created in, and its effective permissions are re-resolved from
-- that user's current membership and role on every request. A key therefore
-- never carries authorization state of its own: changing a role or removing a
-- membership immediately changes what the key can do.
--
-- Only the SHA-256 hash of the key is stored. The prefix and last four
-- characters are kept separately so an operator can identify a key in code
-- without the secret being recoverable.
INSERT INTO permissions (key, description) VALUES
    ('api_keys.manage', 'Create, list, and revoke organization API keys')
ON CONFLICT (key) DO NOTHING;

-- Mints credentials, so it stays with Owner and Admin. Must stay in sync with
-- permissions.DefaultForRole.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.key = 'api_keys.manage'
WHERE r.is_system AND r.name IN ('Owner', 'Admin')
ON CONFLICT DO NOTHING;

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    created_by      UUID NOT NULL,
    name            TEXT NOT NULL CHECK (length(btrim(name)) > 0 AND length(name) <= 100),
    -- Public identifiers for the secret. Neither is enough to authenticate.
    key_prefix      TEXT NOT NULL CHECK (length(key_prefix) > 0),
    key_last_four   TEXT NOT NULL CHECK (length(key_last_four) = 4),
    -- SHA-256 hex digest of the key; the raw key is shown once and never stored.
    key_hash        TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, organization_id),
    CONSTRAINT api_keys_hash_unique UNIQUE (key_hash),
    CONSTRAINT api_keys_expiry_after_creation CHECK (expires_at > created_at),
    -- The key's creator must be a member of the same organization, so a key can
    -- never outlive the membership that backs it: removing the membership
    -- revokes the key with it.
    CONSTRAINT api_keys_creator_organization_fkey FOREIGN KEY (created_by, organization_id)
        REFERENCES organization_memberships (user_id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_api_keys_org_created_at ON api_keys (organization_id, created_at);
CREATE INDEX idx_api_keys_org_active ON api_keys (organization_id) WHERE revoked_at IS NULL;

CREATE TRIGGER trg_api_keys_updated_at
    BEFORE UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY api_keys_isolation ON api_keys
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE api_keys FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON api_keys TO teamflow_app;