DELETE FROM role_permissions WHERE permission_code IN ('loans:collect', 'loans:reports');
DELETE FROM permissions WHERE code IN ('loans:collect', 'loans:reports');
