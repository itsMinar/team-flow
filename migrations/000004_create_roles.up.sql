-- Roles are organization-scoped: the same user can hold different roles in
-- different organizations. is_system marks the default roles created for every
-- organization (Owner, Admin, Manager, Member, Viewer) which cannot be deleted.
CREATE TABLE roles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name            TEXT NOT NULL CHECK (length(btrim(name)) > 0),
    description     TEXT,
    is_system       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE INDEX idx_roles_organization_id ON roles (organization_id);

CREATE TRIGGER trg_roles_updated_at
    BEFORE UPDATE ON roles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
