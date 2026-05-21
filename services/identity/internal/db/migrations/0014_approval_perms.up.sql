-- Phase 7b permissions: maker-checker queue.
--
-- approvals:view  — list / inspect pending approvals.
-- approvals:act   — approve / decline a queued action. Separate from
--                   the action's own permission so a teller can submit
--                   a deposit (savings:transact) but can't approve their
--                   own pending row without approvals:act.

INSERT INTO permissions (code, description, category) VALUES
  ('approvals:view', 'View the cash-approvals queue',  'approvals'),
  ('approvals:act',  'Approve / decline cash actions', 'approvals')
ON CONFLICT (code) DO NOTHING;

-- Re-grant catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- Branch manager + sacco_admin + accountant: full approval authority.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'approvals:view'),
  ('00000000-0000-0000-0000-000000000003', 'approvals:act'),
  ('00000000-0000-0000-0000-000000000004', 'approvals:view'),
  ('00000000-0000-0000-0000-000000000004', 'approvals:act'),
  ('00000000-0000-0000-0000-000000000007', 'approvals:view'),
  ('00000000-0000-0000-0000-000000000007', 'approvals:act')
ON CONFLICT DO NOTHING;

-- Teller + credit_officer can see the queue (so they can find their own
-- pending submissions to cancel), but cannot approve.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000006', 'approvals:view'),
  ('00000000-0000-0000-0000-000000000005', 'approvals:view')
ON CONFLICT DO NOTHING;

-- Auditor: view-only.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000008', 'approvals:view')
ON CONFLICT DO NOTHING;
