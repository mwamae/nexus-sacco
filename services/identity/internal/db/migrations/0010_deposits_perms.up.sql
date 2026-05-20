-- Additional permissions for the deposits sub-module (services/savings).
-- savings:view / savings:transact / savings:approve already exist from
-- migration 0002 — those gate accounts, transactions, and approval.
-- These additions cover the product-config + reversal + snapshot ops.

INSERT INTO permissions (code, description, category) VALUES
  ('deposits:configure', 'Create / edit / archive deposit product configurations', 'savings'),
  ('deposits:reverse',   'Reverse a posted deposit transaction',                   'savings'),
  ('deposits:snapshot',  'Run the daily balance snapshot job',                     'savings')
ON CONFLICT (code) DO NOTHING;

-- sacco_admin: configure products + view (already has savings:view).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'deposits:configure')
ON CONFLICT DO NOTHING;

-- accountant: configure + reverse + snapshot (book-keeping authority).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'deposits:configure'),
  ('00000000-0000-0000-0000-000000000007', 'deposits:reverse'),
  ('00000000-0000-0000-0000-000000000007', 'deposits:snapshot'),
  ('00000000-0000-0000-0000-000000000007', 'savings:view'),
  ('00000000-0000-0000-0000-000000000007', 'savings:approve')
ON CONFLICT DO NOTHING;

-- branch_manager: reverse + already has savings:view + savings:approve.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'deposits:reverse')
ON CONFLICT DO NOTHING;

-- Re-grant platform_admin + tenant_owner catch-alls so existing
-- deployments pick up the new perms.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;
