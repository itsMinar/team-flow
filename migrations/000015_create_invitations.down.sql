DROP TABLE IF EXISTS invitations;

ALTER TABLE roles DROP CONSTRAINT IF EXISTS roles_id_organization_id_key;
DROP INDEX IF EXISTS idx_roles_id_organization_id;