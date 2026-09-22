DROP POLICY IF EXISTS org_isolation ON organizations;
DROP POLICY IF EXISTS role_isolation ON roles;
DROP POLICY IF EXISTS membership_isolation ON organization_memberships;

ALTER TABLE organizations DISABLE ROW LEVEL SECURITY;
ALTER TABLE roles DISABLE ROW LEVEL SECURITY;
ALTER TABLE organization_memberships DISABLE ROW LEVEL SECURITY;
