-- Permissions consumed by services/workflow. Granted by default to the
-- system roles that map to operational seniority. Tenant owners can
-- then create custom roles as needed.

INSERT INTO permissions (code, description, category) VALUES
  ('workflow:view',      'View approval workflows + the approvals inbox', 'workflow'),
  ('workflow:approve',   'Take approval actions on a workflow instance', 'workflow'),
  ('workflow:configure', 'Create / edit workflow definitions', 'workflow')
ON CONFLICT (code) DO NOTHING;

-- platform_admin already gets everything via the catch-all in 0002.
-- We re-run that catch-all here in case 0002 already ran (idempotent).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

-- tenant_owner — everything except platform:*.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin — view + configure + approve.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'workflow:view'),
  ('00000000-0000-0000-0000-000000000003', 'workflow:approve'),
  ('00000000-0000-0000-0000-000000000003', 'workflow:configure')
ON CONFLICT DO NOTHING;

-- branch_manager — view + approve (no configure).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'workflow:view'),
  ('00000000-0000-0000-0000-000000000004', 'workflow:approve')
ON CONFLICT DO NOTHING;

-- credit_officer — view + approve (lending workflows).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000005', 'workflow:view'),
  ('00000000-0000-0000-0000-000000000005', 'workflow:approve')
ON CONFLICT DO NOTHING;

-- accountant — view + approve (for journal posting / period close flows).
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'workflow:view'),
  ('00000000-0000-0000-0000-000000000007', 'workflow:approve')
ON CONFLICT DO NOTHING;

-- auditor — view only.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000008', 'workflow:view')
ON CONFLICT DO NOTHING;
