DROP TABLE IF EXISTS activity_logs;
DROP TABLE IF EXISTS projects;

DELETE FROM permissions
WHERE key IN ('projects.read', 'projects.create', 'projects.update', 'projects.delete');
