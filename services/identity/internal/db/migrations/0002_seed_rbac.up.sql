-- Seed the global permission catalog and the system roles each new
-- tenant inherits. Tenant-specific roles are created at runtime.

-- ───────── Permissions ─────────
INSERT INTO permissions (code, description, category) VALUES
  -- Platform (super-admin only)
  ('platform:tenants:view',    'View all tenants',                   'platform'),
  ('platform:tenants:create',  'Create a new tenant',                'platform'),
  ('platform:tenants:edit',    'Edit any tenant',                    'platform'),
  ('platform:tenants:suspend', 'Suspend / reactivate a tenant',      'platform'),

  -- Tenant settings
  ('tenant:settings:view',     'View tenant settings',               'tenant'),
  ('tenant:settings:edit',     'Edit tenant settings & branding',    'tenant'),

  -- Users & RBAC
  ('users:view',               'View users in this tenant',          'users'),
  ('users:invite',             'Invite / create users',              'users'),
  ('users:edit',               'Edit users',                         'users'),
  ('users:suspend',            'Suspend / reactivate users',         'users'),
  ('roles:view',               'View roles',                         'users'),
  ('roles:edit',               'Create / edit / assign roles',       'users'),

  -- Members
  ('members:view',             'View members',                       'members'),
  ('members:create',           'Onboard new members',                'members'),
  ('members:edit',             'Edit member records',                'members'),
  ('members:approve',          'Approve member onboarding',          'members'),

  -- Savings
  ('savings:view',             'View savings accounts',              'savings'),
  ('savings:transact',         'Process deposits / withdrawals',     'savings'),
  ('savings:approve',          'Approve high-value transactions',    'savings'),

  -- Loans
  ('loans:view',               'View loans',                         'loans'),
  ('loans:originate',          'Create loan applications',           'loans'),
  ('loans:underwrite',         'Run underwriting / risk',            'loans'),
  ('loans:approve',            'Approve loans (within authority)',   'loans'),
  ('loans:disburse',           'Disburse approved loans',            'loans'),
  ('loans:restructure',        'Restructure / reschedule loans',     'loans'),

  -- Collections
  ('collections:view',         'View collections workdesk',          'collections'),
  ('collections:act',          'Log contacts, set promises',         'collections'),

  -- Accounting
  ('accounting:view',          'View GL, journals, reports',         'accounting'),
  ('accounting:post',          'Post manual journal entries',        'accounting'),
  ('accounting:close',         'Run period close',                   'accounting'),

  -- Reports
  ('reports:view',             'View standard reports',              'reports'),
  ('reports:export',           'Export reports',                     'reports'),

  -- Audit
  ('audit:view',               'View audit log',                     'audit')
ON CONFLICT (code) DO NOTHING;

-- ───────── System roles (tenant_id IS NULL — templates) ─────────
INSERT INTO roles (id, tenant_id, code, name, description, is_system) VALUES
  ('00000000-0000-0000-0000-000000000001', NULL, 'platform_admin',
   'Platform Super Admin', 'Cross-tenant platform operator', true),
  ('00000000-0000-0000-0000-000000000002', NULL, 'tenant_owner',
   'Tenant Owner', 'Full control over a single tenant', true),
  ('00000000-0000-0000-0000-000000000003', NULL, 'sacco_admin',
   'SACCO Admin', 'Operations & user management', true),
  ('00000000-0000-0000-0000-000000000004', NULL, 'branch_manager',
   'Branch Manager', 'Branch operations and second-line approvals', true),
  ('00000000-0000-0000-0000-000000000005', NULL, 'credit_officer',
   'Credit Officer', 'Loan origination, underwriting, first-line approvals', true),
  ('00000000-0000-0000-0000-000000000006', NULL, 'teller',
   'Teller', 'Cash & teller-line transactions', true),
  ('00000000-0000-0000-0000-000000000007', NULL, 'accountant',
   'Accountant', 'General ledger and reconciliation', true),
  ('00000000-0000-0000-0000-000000000008', NULL, 'auditor',
   'Auditor', 'Read-only audit access', true),
  ('00000000-0000-0000-0000-000000000009', NULL, 'collections_officer',
   'Collections Officer', 'Delinquent loan workdesk', true),
  ('00000000-0000-0000-0000-00000000000a', NULL, 'member',
   'Member', 'Self-service member access', true)
ON CONFLICT (tenant_id, code) DO NOTHING;

-- ───────── Default role → permission mapping ─────────
-- platform_admin gets every permission (filtered out at runtime by is_platform_admin).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

-- tenant_owner: everything except platform:*
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin: tenant settings + users + most ops, no high-value approve, no period close.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'tenant:settings:view'),
  ('00000000-0000-0000-0000-000000000003', 'tenant:settings:edit'),
  ('00000000-0000-0000-0000-000000000003', 'users:view'),
  ('00000000-0000-0000-0000-000000000003', 'users:invite'),
  ('00000000-0000-0000-0000-000000000003', 'users:edit'),
  ('00000000-0000-0000-0000-000000000003', 'users:suspend'),
  ('00000000-0000-0000-0000-000000000003', 'roles:view'),
  ('00000000-0000-0000-0000-000000000003', 'roles:edit'),
  ('00000000-0000-0000-0000-000000000003', 'members:view'),
  ('00000000-0000-0000-0000-000000000003', 'members:approve'),
  ('00000000-0000-0000-0000-000000000003', 'savings:view'),
  ('00000000-0000-0000-0000-000000000003', 'loans:view'),
  ('00000000-0000-0000-0000-000000000003', 'collections:view'),
  ('00000000-0000-0000-0000-000000000003', 'accounting:view'),
  ('00000000-0000-0000-0000-000000000003', 'reports:view'),
  ('00000000-0000-0000-0000-000000000003', 'reports:export'),
  ('00000000-0000-0000-0000-000000000003', 'audit:view')
ON CONFLICT DO NOTHING;

-- branch_manager
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'members:view'),
  ('00000000-0000-0000-0000-000000000004', 'members:approve'),
  ('00000000-0000-0000-0000-000000000004', 'savings:view'),
  ('00000000-0000-0000-0000-000000000004', 'savings:approve'),
  ('00000000-0000-0000-0000-000000000004', 'loans:view'),
  ('00000000-0000-0000-0000-000000000004', 'loans:approve'),
  ('00000000-0000-0000-0000-000000000004', 'collections:view'),
  ('00000000-0000-0000-0000-000000000004', 'reports:view')
ON CONFLICT DO NOTHING;

-- credit_officer
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000005', 'members:view'),
  ('00000000-0000-0000-0000-000000000005', 'loans:view'),
  ('00000000-0000-0000-0000-000000000005', 'loans:originate'),
  ('00000000-0000-0000-0000-000000000005', 'loans:underwrite'),
  ('00000000-0000-0000-0000-000000000005', 'loans:approve'),
  ('00000000-0000-0000-0000-000000000005', 'collections:view'),
  ('00000000-0000-0000-0000-000000000005', 'collections:act')
ON CONFLICT DO NOTHING;

-- teller
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000006', 'members:view'),
  ('00000000-0000-0000-0000-000000000006', 'members:create'),
  ('00000000-0000-0000-0000-000000000006', 'savings:view'),
  ('00000000-0000-0000-0000-000000000006', 'savings:transact'),
  ('00000000-0000-0000-0000-000000000006', 'loans:view')
ON CONFLICT DO NOTHING;

-- accountant
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'accounting:view'),
  ('00000000-0000-0000-0000-000000000007', 'accounting:post'),
  ('00000000-0000-0000-0000-000000000007', 'accounting:close'),
  ('00000000-0000-0000-0000-000000000007', 'reports:view'),
  ('00000000-0000-0000-0000-000000000007', 'reports:export')
ON CONFLICT DO NOTHING;

-- auditor — read everything operational
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000008', 'members:view'),
  ('00000000-0000-0000-0000-000000000008', 'savings:view'),
  ('00000000-0000-0000-0000-000000000008', 'loans:view'),
  ('00000000-0000-0000-0000-000000000008', 'collections:view'),
  ('00000000-0000-0000-0000-000000000008', 'accounting:view'),
  ('00000000-0000-0000-0000-000000000008', 'reports:view'),
  ('00000000-0000-0000-0000-000000000008', 'reports:export'),
  ('00000000-0000-0000-0000-000000000008', 'audit:view')
ON CONFLICT DO NOTHING;

-- collections_officer
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000009', 'members:view'),
  ('00000000-0000-0000-0000-000000000009', 'loans:view'),
  ('00000000-0000-0000-0000-000000000009', 'collections:view'),
  ('00000000-0000-0000-0000-000000000009', 'collections:act')
ON CONFLICT DO NOTHING;

-- member (self-service) — no admin permissions; relies on member-facing endpoints.
