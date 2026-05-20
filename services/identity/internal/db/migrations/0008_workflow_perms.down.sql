DELETE FROM role_permissions WHERE permission_code IN ('workflow:view', 'workflow:approve', 'workflow:configure');
DELETE FROM permissions WHERE code IN ('workflow:view', 'workflow:approve', 'workflow:configure');
