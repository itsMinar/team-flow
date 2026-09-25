DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE key IN ('teams.read', 'teams.manage'));
DELETE FROM permissions WHERE key IN ('teams.read', 'teams.manage');