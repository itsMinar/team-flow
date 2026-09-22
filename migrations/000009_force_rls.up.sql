-- Phase 3: make RLS effective for the table owner the application connects as.
-- Without FORCE, PostgreSQL bypasses row security for a table's owner, so the
-- policies from migration 8 never applied to application traffic. FORCE ensures
-- the app.current_org_id context set per transaction actually constrains rows.
-- The policies remain permissive when no context is set (login, registration,
-- organization switching), so unscoped operations keep working.
ALTER TABLE organizations FORCE ROW LEVEL SECURITY;
ALTER TABLE roles FORCE ROW LEVEL SECURITY;
ALTER TABLE organization_memberships FORCE ROW LEVEL SECURITY;
