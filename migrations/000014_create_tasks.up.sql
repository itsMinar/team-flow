-- Phase 7: project-scoped tasks with assignment, statuses, priorities, and
-- due dates. Task permissions mirror the project permissions so RBAC stays
-- consistent across the two resources.
INSERT INTO permissions (key, description) VALUES
    ('tasks.read', 'Read organization tasks and task activity'),
    ('tasks.create', 'Create tasks in projects'),
    ('tasks.update', 'Update tasks, including assignment'),
    ('tasks.delete', 'Delete tasks')
ON CONFLICT (key) DO NOTHING;

-- Backfills task permissions for system roles of organizations that already
-- exist. Must stay in sync with permissions.DefaultForRole.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
JOIN permissions p ON (
    (r.name IN ('Owner', 'Admin') AND p.key IN (
        'tasks.read', 'tasks.create', 'tasks.update', 'tasks.delete'))
    OR (r.name = 'Manager' AND p.key IN (
        'tasks.read', 'tasks.create', 'tasks.update'))
    OR (r.name IN ('Member', 'Viewer') AND p.key = 'tasks.read')
)
WHERE r.is_system
ON CONFLICT DO NOTHING;

CREATE TABLE tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    project_id      UUID NOT NULL,
    title           TEXT NOT NULL CHECK (length(btrim(title)) > 0 AND length(title) <= 200),
    description     TEXT CHECK (description IS NULL OR length(description) <= 5000),
    status          TEXT NOT NULL DEFAULT 'todo'
                    CHECK (status IN ('todo', 'in_progress', 'blocked', 'in_review', 'done', 'cancelled')),
    priority        TEXT NOT NULL DEFAULT 'medium'
                    CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
    assignee_id     UUID,
    due_date        DATE,
    created_by      UUID NOT NULL REFERENCES users (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, organization_id),
    -- A task's project must live in the same organization; deleting a project
    -- removes its tasks.
    CONSTRAINT tasks_project_fkey FOREIGN KEY (project_id, organization_id)
        REFERENCES projects (id, organization_id) ON DELETE CASCADE,
    -- The assignee must hold a membership in the same organization. Removing a
    -- member unassigns their tasks instead of deleting them.
    CONSTRAINT tasks_assignee_fkey FOREIGN KEY (assignee_id, organization_id)
        REFERENCES organization_memberships (user_id, organization_id) ON DELETE SET NULL (assignee_id)
);

CREATE INDEX idx_tasks_org_project ON tasks (organization_id, project_id);
CREATE INDEX idx_tasks_org_status ON tasks (organization_id, status);
CREATE INDEX idx_tasks_org_priority ON tasks (organization_id, priority);
CREATE INDEX idx_tasks_org_assignee ON tasks (organization_id, assignee_id);
CREATE INDEX idx_tasks_org_created_at ON tasks (organization_id, created_at);
CREATE INDEX idx_tasks_project ON tasks (project_id, organization_id);

CREATE TRIGGER trg_tasks_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tasks_isolation ON tasks
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE tasks FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON tasks TO teamflow_app;
