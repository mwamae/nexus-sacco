-- Permissions for the shares sub-module (services/savings).
-- The existing `savings:*` codes are reserved for the deposit-side
-- engine that ships in Phase 3; shares get their own namespace so the
-- two can be granted independently.

INSERT INTO permissions (code, description, category) VALUES
  ('shares:view',         'View share accounts, transactions, and certificates', 'shares'),
  ('shares:buy',          'Process member share purchases',                       'shares'),
  ('shares:transfer',     'Transfer shares between members',                      'shares'),
  ('shares:redeem',       'Redeem shares (member exit / buyback)',                'shares'),
  ('shares:adjust',       'Post administrative share adjustments',                'shares'),
  ('shares:bonus_issue',  'Run AGM-approved bonus share issues',                  'shares'),
  ('shares:lien',         'Place or release liens on member shares',              'shares'),
  ('dividends:view',      'View dividend runs and member entitlements',           'shares'),
  ('dividends:run',       'Run dividend computation and produce preview',         'shares'),
  ('dividends:approve',   'Approve and post a dividend run',                      'shares')
ON CONFLICT (code) DO NOTHING;

-- Grant to system roles. tenant_owner already has everything-except-platform
-- via the WHERE-category filter, so it picks these up automatically.

-- sacco_admin: full visibility, can run buy/transfer/lien but not bonus or redeem
-- (those are sensitive enough to want a second pair of eyes — board approval).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'shares:view'),
  ('00000000-0000-0000-0000-000000000003', 'shares:buy'),
  ('00000000-0000-0000-0000-000000000003', 'shares:transfer'),
  ('00000000-0000-0000-0000-000000000003', 'shares:adjust'),
  ('00000000-0000-0000-0000-000000000003', 'shares:lien'),
  ('00000000-0000-0000-0000-000000000003', 'dividends:view')
ON CONFLICT DO NOTHING;

-- branch_manager: view + buy + lien (typical front-office capability).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'shares:view'),
  ('00000000-0000-0000-0000-000000000004', 'shares:buy'),
  ('00000000-0000-0000-0000-000000000004', 'shares:lien'),
  ('00000000-0000-0000-0000-000000000004', 'dividends:view')
ON CONFLICT DO NOTHING;

-- teller: view + buy (cash counter share purchases).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000006', 'shares:view'),
  ('00000000-0000-0000-0000-000000000006', 'shares:buy')
ON CONFLICT DO NOTHING;

-- accountant: full dividend lifecycle (run + approve).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'shares:view'),
  ('00000000-0000-0000-0000-000000000007', 'shares:adjust'),
  ('00000000-0000-0000-0000-000000000007', 'dividends:view'),
  ('00000000-0000-0000-0000-000000000007', 'dividends:run'),
  ('00000000-0000-0000-0000-000000000007', 'dividends:approve')
ON CONFLICT DO NOTHING;

-- auditor: read-only across the module.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000008', 'shares:view'),
  ('00000000-0000-0000-0000-000000000008', 'dividends:view')
ON CONFLICT DO NOTHING;

-- platform_admin + tenant_owner are filled by the catch-all grants in
-- 0002_seed_rbac.up.sql, but those only run on initial seed. Re-apply
-- them so existing deployments pick up the new perms.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;
