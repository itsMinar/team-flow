INSERT INTO permissions (key, description) VALUES
    ('teams.read', 'Read organization teams and team members'),
    ('teams.manage', 'Create, update, delete, and manage team members')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Owner', 'Admin')
  AND p.key IN ('teams.read', 'teams.manage')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM roles r
CROSS JOIN permissions p
WHERE r.name IN ('Manager', 'Member', 'Viewer')
  AND p.key = 'teams.read'
ON CONFLICT DO NOTHING;

CREATE TABLE teams (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            TEXT NOT NULL CHECK (length(btrim(name)) > 0),
    description     TEXT,
    created_by      UUID NOT NULL REFERENCES users (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (id, organization_id)
);

CREATE INDEX idx_teams_organization_id ON teams (organization_id);
CREATE INDEX idx_teams_organization_created_at ON teams (organization_id, created_at);

CREATE TRIGGER trg_teams_updated_at
    BEFORE UPDATE ON teams
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE team_members (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    team_id         UUID NOT NULL,
    user_id         UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, team_id, user_id),
    FOREIGN KEY (team_id, organization_id) REFERENCES teams (id, organization_id) ON DELETE CASCADE
);

CREATE INDEX idx_team_members_organization_id ON team_members (organization_id);
CREATE INDEX idx_team_members_team_id ON team_members (team_id);
CREATE INDEX idx_team_members_user_id ON team_members (user_id);

ALTER TABLE teams ENABLE ROW LEVEL SECURITY;
CREATE POLICY teams_isolation ON teams
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE teams FORCE ROW LEVEL SECURITY;

ALTER TABLE team_members ENABLE ROW LEVEL SECURITY;
CREATE POLICY team_members_isolation ON team_members
    USING (
        current_setting('app.current_org_id', true) = ''
        OR current_setting('app.current_org_id', true) IS NULL
        OR organization_id::text = current_setting('app.current_org_id', true)
    );
ALTER TABLE team_members FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON teams, team_members TO teamflow_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO teamflow_app;