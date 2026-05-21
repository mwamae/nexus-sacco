-- Permissions for the lending module (Phase 6).
-- loans:* existed from migration 0002 but were generic. These add the
-- granular gates the lending handlers need.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:configure',  'Create / edit / archive loan product configurations',     'loans'),
  ('loans:apply',      'Submit loan applications on behalf of members',           'loans'),
  ('loans:guarantee',  'Consent (accept / decline) on guarantee requests',        'loans'),
  ('loans:assess',     'Run / re-run credit scoring + affordability assessment',  'loans'),
  ('loans:offer',      'Generate offer letters and accept / decline offers',      'loans'),
  ('loans:disburse',   'Authorise and execute disbursement (extends existing)',   'loans'),
  ('loans:reverse',    'Reverse a posted loan transaction',                       'loans')
ON CONFLICT (code) DO NOTHING;

-- credit_officer: full origination + servicing capability except final approval.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000005', 'loans:apply'),
  ('00000000-0000-0000-0000-000000000005', 'loans:assess'),
  ('00000000-0000-0000-0000-000000000005', 'loans:offer'),
  ('00000000-0000-0000-0000-000000000005', 'loans:disburse')
ON CONFLICT DO NOTHING;

-- branch_manager: also gets configure (already had loans:approve from 0002).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'loans:configure'),
  ('00000000-0000-0000-0000-000000000004', 'loans:offer'),
  ('00000000-0000-0000-0000-000000000004', 'loans:disburse')
ON CONFLICT DO NOTHING;

-- sacco_admin: configuration authority + product management.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'loans:configure'),
  ('00000000-0000-0000-0000-000000000003', 'loans:originate'),
  ('00000000-0000-0000-0000-000000000003', 'loans:underwrite'),
  ('00000000-0000-0000-0000-000000000003', 'loans:approve'),
  ('00000000-0000-0000-0000-000000000003', 'loans:disburse')
ON CONFLICT DO NOTHING;

-- accountant: reversal authority.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'loans:view'),
  ('00000000-0000-0000-0000-000000000007', 'loans:reverse')
ON CONFLICT DO NOTHING;

-- members get loans:guarantee so they can accept/decline guarantee requests
-- through the member-facing surface (when one ships).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-00000000000a', 'loans:guarantee')
ON CONFLICT DO NOTHING;

-- Re-grant platform_admin + tenant_owner catch-alls so existing
-- deployments inherit the new perms.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;
