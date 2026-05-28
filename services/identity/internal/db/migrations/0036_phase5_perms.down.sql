DELETE FROM role_permissions
 WHERE permission_code IN (
   'loans:topup','loans:refinance',
   'loans:checkoff:upload','loans:checkoff:post',
   'loans:view:insider'
 );

DELETE FROM permissions
 WHERE code IN (
   'loans:topup','loans:refinance',
   'loans:checkoff:upload','loans:checkoff:post',
   'loans:view:insider'
 );
