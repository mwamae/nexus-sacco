DELETE FROM role_permissions
 WHERE permission_code IN (
   'loans:charge_registration','loans:insurance_record','loans:custody','loans:auction'
 );

DELETE FROM permissions
 WHERE code IN (
   'loans:charge_registration','loans:insurance_record','loans:custody','loans:auction'
 );
