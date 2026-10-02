-- Supports role membership counts and efficient role-delete foreign-key checks.
CREATE INDEX idx_memberships_role_org
    ON organization_memberships (role_id, organization_id);