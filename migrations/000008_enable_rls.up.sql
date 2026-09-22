-- Phase 3: defense-in-depth Row Level Security.
-- Application code always scopes by organization_id; these policies add a
-- database-level backstop when app.current_org_id is set for a transaction.
-- When no org context is set (login, registration, org switching), rows remain
-- visible so application-level membership checks stay authoritative.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'teamflow_app') THEN
        CREATE ROLE teamflow_app NOLOGIN;
    END IF;
END
$$;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO teamflow_app;

ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE organization_memberships ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS org_isolation ON organizations;
CREATE POLICY org_isolation ON organizations
    USING (
        current_setting('app.current_org_id', true) IN ('', id::text)
        OR current_setting('app.current_org_id', true) IS NULL
    );

DROP POLICY IF EXISTS role_isolation ON roles;
CREATE POLICY role_isolation ON roles
    USING (
        current_setting('app.current_org_id', true) IN ('', organization_id::text)
        OR current_setting('app.current_org_id', true) IS NULL
    );

DROP POLICY IF EXISTS membership_isolation ON organization_memberships;
CREATE POLICY membership_isolation ON organization_memberships
    USING (
        current_setting('app.current_org_id', true) IN ('', organization_id::text)
        OR current_setting('app.current_org_id', true) IS NULL
    );
