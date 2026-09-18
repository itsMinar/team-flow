-- A user's membership in an organization, carrying their role there. The unique
-- constraint prevents duplicate membership of a user in the same organization.
CREATE TABLE organization_memberships (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role_id         UUID NOT NULL REFERENCES roles (id),
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'invited', 'suspended', 'removed')),
    joined_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, user_id)
);

CREATE INDEX idx_memberships_user_id ON organization_memberships (user_id);
CREATE INDEX idx_memberships_organization_id ON organization_memberships (organization_id);
CREATE INDEX idx_memberships_org_status ON organization_memberships (organization_id, status);

CREATE TRIGGER trg_memberships_updated_at
    BEFORE UPDATE ON organization_memberships
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
