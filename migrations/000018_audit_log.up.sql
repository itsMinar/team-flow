-- Phase 12: append-only security audit log.
--
-- The audit log answers "who did what, from where, and when" for security-relevant
-- actions. It is deliberately separate from activity_logs, which describes
-- user-facing resource history: activity is about what happened to a project, the
-- audit log is about who authenticated, who changed access, and which credentials
-- were minted.
--
-- organization_id intentionally has no foreign key. Cascading audit rows away when
-- an organization is deleted would let a tenant owner erase the record of their own
-- access changes, which defeats the purpose of keeping an audit trail. Rows keep the
-- identifier and simply stop being visible once the organization is gone.
--
-- actor_user_id keeps a foreign key with ON DELETE SET NULL so the row survives a
-- deleted account while still recording that an account was involved.
--
-- The application role may insert and select only: the log is append-only, so an
-- operator or an application bug cannot rewrite history.
INSERT INTO permissions (key, description) VALUES
    ('audit.read', 'Read the organization audit log')
ON CONFLICT (key) DO NOTHING;

-- Audit history is administrative. Must stay in sync with permissions.DefaultForRole.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.key = 'audit.read'
WHERE r.is_system AND r.name IN ('Owner', 'Admin')
ON CONFLICT DO NOTHING;

CREATE TABLE audit_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Nullable: some events, such as a failed login, happen before any tenant is
    -- known. Rows without an organization are never exposed through a tenant.
    organization_id UUID,
    actor_user_id   UUID REFERENCES users (id) ON DELETE SET NULL,
    action          TEXT NOT NULL CHECK (length(btrim(action)) > 0),
    outcome         TEXT NOT NULL DEFAULT 'success'
                    CHECK (outcome IN ('success', 'failure', 'denied')),
    -- What the action was aimed at, such as a membership or an API key.
    target_type     TEXT,
    target_id       TEXT,
    ip_address      INET,
    user_agent      TEXT,
    request_id      TEXT,
    trace_id        TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_org_created_at ON audit_logs (organization_id, created_at DESC);
CREATE INDEX idx_audit_action_created_at ON audit_logs (action, created_at DESC);
CREATE INDEX idx_audit_actor_created_at ON audit_logs (actor_user_id, created_at DESC);

ALTER TABLE audit_logs ENABLE ROW LEVEL SECURITY;

-- Reading follows the same shape as every other tenant-owned table: visible when no
-- tenant context is set, and confined to the active tenant when one is. The service
-- always filters by organization_id, so the RLS clause is the backstop rather than
-- the only defence.
--
-- Note that a session which has run a tenant transaction reports an empty setting
-- rather than no setting, so both forms have to be treated as "no tenant".
-- Rows without an organization are never returned by the API, because the query
-- filters on organization_id; they exist for operator queries.
CREATE POLICY audit_logs_select ON audit_logs FOR SELECT
    USING (
        current_setting('app.current_org_id', true) IS NULL
        OR current_setting('app.current_org_id', true) = ''
        OR organization_id::text = current_setting('app.current_org_id', true)
    );

-- Writing is allowed for the active tenant and for events with no tenant at all,
-- which is what a login attempt against an unknown account looks like.
CREATE POLICY audit_logs_insert ON audit_logs FOR INSERT
    WITH CHECK (
        current_setting('app.current_org_id', true) IS NULL
        OR current_setting('app.current_org_id', true) = ''
        OR organization_id IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );

ALTER TABLE audit_logs FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT ON audit_logs TO teamflow_app;
-- The audit log is append-only: no updates, no deletes, ever.
REVOKE UPDATE, DELETE ON audit_logs FROM teamflow_app;