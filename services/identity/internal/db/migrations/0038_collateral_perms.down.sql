DELETE FROM role_permissions
 WHERE permission_code IN (
   'loans:verify_collateral','loans:value_collateral','loans:override_coverage'
 );

DELETE FROM permissions
 WHERE code IN (
   'loans:verify_collateral','loans:value_collateral','loans:override_coverage'
 );
