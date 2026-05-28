DELETE FROM role_permissions
 WHERE permission_code IN (
   'loans:provisioning:run',
   'loans:provisioning:post',
   'loans:policy:write'
 );

DELETE FROM permissions
 WHERE code IN (
   'loans:provisioning:run',
   'loans:provisioning:post',
   'loans:policy:write'
 );
