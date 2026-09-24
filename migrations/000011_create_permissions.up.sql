CREATE TABLE permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT NOT NULL UNIQUE CHECK (length(btrim(key)) > 0),
    description TEXT NOT NULL CHECK (length(btrim(description)) > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_permissions (
    role_id       UUID NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE INDEX idx_role_permissions_permission_id ON role_permissions (permission_id);

INSERT INTO permissions (key, description) VALUES
    ('organizations.read', 'Read organization details'),
    ('organizations.update', 'Rename the organization'),
    ('members.read', 'Read organization members'),
    ('members.manage', 'Assign and manage member roles'),
    ('roles.read', 'Read organization roles and permissions'),
    ('roles.manage', 'Create, update, and delete custom roles')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Owner', 'Admin')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON p.key IN (
    'organizations.read',
    'members.read',
    'roles.read'
)
WHERE r.name IN ('Manager', 'Member', 'Viewer')
ON CONFLICT DO NOTHING;

GRANT SELECT, INSERT, UPDATE, DELETE ON permissions, role_permissions TO teamflow_app;

ALTER TABLE role_permissions ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS role_permission_isolation ON role_permissions;
CREATE POLICY role_permission_isolation ON role_permissions
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR EXISTS (
            SELECT 1
            FROM roles
            WHERE roles.id = role_permissions.role_id
              AND roles.organization_id::text = current_setting('app.current_org_id', true)
        )
    );

ALTER TABLE role_permissions FORCE ROW LEVEL SECURITY;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO teamflow_app;