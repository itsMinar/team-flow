DROP TABLE IF EXISTS tasks;

DELETE FROM permissions
WHERE key IN ('tasks.read', 'tasks.create', 'tasks.update', 'tasks.delete');
