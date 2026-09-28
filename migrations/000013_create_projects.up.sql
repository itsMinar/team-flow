INSERT INTO permissions (key, description) VALUES
    ('projects.read', 'Read organization projects and project activity'),
    ('projects.create', 'Create projects'),
    ('projects.update', 'Update projects'),
    ('projects.delete', 'Delete projects')
ON CONFLICT (key) DO NOTHING;

-- Also backfills team permissions for system roles created by registration after migration 12.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON (
    (r.name IN ('Owner', 'Admin') AND p.key IN (
        'teams.read', 'teams.manage',
        'projects.read', 'projects.create', 'projects.update', 'projects.delete'))
    OR (r.name = 'Manager' AND p.key IN (
        'teams.read', 'projects.read', 'projects.create', 'projects.update'))
    OR (r.name IN ('Member', 'Viewer') AND p.key IN ('teams.read', 'projects.read'))
)
WHERE r.is_system
ON CONFLICT DO NOTHING;

CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    team_id         UUID,
    name            TEXT NOT NULL CHECK (length(btrim(name)) > 0 AND length(name) <= 150),
    description     TEXT CHECK (description IS NULL OR length(description) <= 5000),
    status          TEXT NOT NULL DEFAULT 'planning'
                    CHECK (status IN ('planning', 'active', 'on_hold', 'completed', 'archived')),
    priority        TEXT NOT NULL DEFAULT 'medium'
                    CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
    start_date      DATE,
    due_date        DATE,
    created_by      UUID NOT NULL REFERENCES users (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT projects_due_after_start CHECK (
        start_date IS NULL OR due_date IS NULL OR due_date >= start_date),
    UNIQUE (id, organization_id),
    -- Composite key keeps a project's team in the same organization; deleting a team only unassigns it.
    CONSTRAINT projects_team_fkey FOREIGN KEY (team_id, organization_id)
        REFERENCES teams (id, organization_id) ON DELETE SET NULL (team_id)
);

CREATE INDEX idx_projects_org_created_at ON projects (organization_id, created_at);
CREATE INDEX idx_projects_org_status ON projects (organization_id, status);
CREATE INDEX idx_projects_org_priority ON projects (organization_id, priority);
CREATE INDEX idx_projects_team ON projects (team_id, organization_id);

CREATE TRIGGER trg_projects_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE activity_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    actor_user_id   UUID REFERENCES users (id) ON DELETE SET NULL,
    action          TEXT NOT NULL CHECK (length(btrim(action)) > 0),
    resource_type   TEXT NOT NULL CHECK (length(btrim(resource_type)) > 0),
    resource_id     UUID NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_activity_org_resource
    ON activity_logs (organization_id, resource_type, resource_id, created_at DESC);
CREATE INDEX idx_activity_org_created_at ON activity_logs (organization_id, created_at);

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY projects_isolation ON projects
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE projects FORCE ROW LEVEL SECURITY;

ALTER TABLE activity_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY activity_logs_isolation ON activity_logs
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE activity_logs FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON projects TO teamflow_app;
-- Activity is append-only for the application role.
GRANT SELECT, INSERT ON activity_logs TO teamflow_app;
REVOKE UPDATE, DELETE ON activity_logs FROM teamflow_app;
