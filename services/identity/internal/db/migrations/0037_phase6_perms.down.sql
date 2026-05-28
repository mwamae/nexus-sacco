DELETE FROM role_permissions
 WHERE permission_code IN (
   'loans:crb:pull','loans:insurance:configure','loans:insurance:claim',
   'members:self_service:enable','portal:self'
 );

DELETE FROM permissions
 WHERE code IN (
   'loans:crb:pull','loans:insurance:configure','loans:insurance:claim',
   'members:self_service:enable','portal:self'
 );
