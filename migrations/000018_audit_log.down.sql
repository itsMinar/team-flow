DROP TABLE IF EXISTS audit_logs;

DELETE FROM permissions WHERE key = 'audit.read';
