DELETE FROM role_permissions WHERE permission_code IN ('loans:sasra', 'loans:reports:export');
DELETE FROM permissions WHERE code IN ('loans:sasra', 'loans:reports:export');
