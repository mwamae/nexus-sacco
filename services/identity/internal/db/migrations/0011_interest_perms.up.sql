-- Permissions for the interest engine (Phase 4).
-- interest:view   — see runs, lines, WHT reports
-- interest:run    — create draft, edit, compute, submit for approval
-- interest:approve — approve a preview (or pull through workflow)
-- interest:post   — execute posting + lock the run

INSERT INTO permissions (code, description, category) VALUES
  ('interest:view',    'View interest runs, lines, WHT schedules and certificates', 'savings'),
  ('interest:run',     'Create / edit / compute / submit interest runs',             'savings'),
  ('interest:approve', 'Approve interest-run preview (compliance / board)',          'savings'),
  ('interest:post',    'Execute posting and lock an approved interest run',          'savings')
ON CONFLICT (code) DO NOTHING;

-- accountant: full lifecycle owner.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'interest:view'),
  ('00000000-0000-0000-0000-000000000007', 'interest:run'),
  ('00000000-0000-0000-0000-000000000007', 'interest:post')
ON CONFLICT DO NOTHING;

-- sacco_admin: view + can also approve at the senior level.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'interest:view'),
  ('00000000-0000-0000-0000-000000000003', 'interest:approve')
ON CONFLICT DO NOTHING;

-- branch_manager + auditor: read-only.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'interest:view'),
  ('00000000-0000-0000-0000-000000000008', 'interest:view')
ON CONFLICT DO NOTHING;

-- Re-apply catch-alls so existing platform_admin / tenant_owner roles
-- inherit the new permissions.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;
