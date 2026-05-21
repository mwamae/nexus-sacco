DELETE FROM role_permissions WHERE permission_code IN (
  'loans:configure', 'loans:apply', 'loans:guarantee', 'loans:assess',
  'loans:offer', 'loans:disburse', 'loans:reverse'
);
DELETE FROM permissions WHERE code IN (
  'loans:configure', 'loans:apply', 'loans:guarantee', 'loans:assess',
  'loans:offer', 'loans:disburse', 'loans:reverse'
);
