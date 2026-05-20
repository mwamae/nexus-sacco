DELETE FROM role_permissions WHERE permission_code IN ('deposits:configure', 'deposits:reverse', 'deposits:snapshot');
DELETE FROM permissions WHERE code IN ('deposits:configure', 'deposits:reverse', 'deposits:snapshot');
