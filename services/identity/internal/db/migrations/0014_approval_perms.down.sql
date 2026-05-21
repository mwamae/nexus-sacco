DELETE FROM role_permissions WHERE permission_code IN ('approvals:view', 'approvals:act');
DELETE FROM permissions WHERE code IN ('approvals:view', 'approvals:act');
