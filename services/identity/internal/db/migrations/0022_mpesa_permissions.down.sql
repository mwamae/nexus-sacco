DELETE FROM role_permissions WHERE permission_code IN (
  'mpesa:paybill:manage',
  'mpesa:credentials:rotate',
  'mpesa:reconcile:run'
);
DELETE FROM permissions WHERE code IN (
  'mpesa:paybill:manage',
  'mpesa:credentials:rotate',
  'mpesa:reconcile:run'
);
