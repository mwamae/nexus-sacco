DELETE FROM role_permissions
 WHERE permission_code IN ('loans:collect:assign','loans:collect:legal');

DELETE FROM permissions
 WHERE code IN ('loans:collect:assign','loans:collect:legal');
