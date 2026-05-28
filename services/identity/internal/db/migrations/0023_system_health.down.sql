DELETE FROM role_permissions WHERE permission_code = 'tenant:operations:view';
DELETE FROM permissions WHERE code = 'tenant:operations:view';
DROP TABLE IF EXISTS worker_heartbeats;
