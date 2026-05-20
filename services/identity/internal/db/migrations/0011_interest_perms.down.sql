DELETE FROM role_permissions WHERE permission_code IN ('interest:view', 'interest:run', 'interest:approve', 'interest:post');
DELETE FROM permissions WHERE code IN ('interest:view', 'interest:run', 'interest:approve', 'interest:post');
