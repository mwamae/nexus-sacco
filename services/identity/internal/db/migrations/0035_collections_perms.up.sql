-- Loans Phase 4 — collections sub-permissions.
--
-- loans:collect (seeded in 0032) already gates the collections
-- workspace. Phase 4 adds two narrower perms for the more sensitive
-- actions:
--
--   loans:collect:assign — assign / unassign loans to officers.
--                          Held by branch_manager + sacco_admin
--                          (supervisor-level call).
--
--   loans:collect:legal  — hand a loan over to the legal team.
--                          Held by sacco_admin only (board-level
--                          call — branch_manager intentionally
--                          excluded; legal handover is irreversible
--                          and changes the SACCO's recovery posture).
--
-- Platform_admin + tenant_owner inherit via the standard catch-all.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:collect:assign', 'Assign or unassign loans to collections officers', 'loans'),
  ('loans:collect:legal',  'Hand a loan over to the legal recovery team',     'loans')
ON CONFLICT (code) DO NOTHING;

-- Catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin — both.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'loans:collect:assign'),
  ('00000000-0000-0000-0000-000000000003', 'loans:collect:legal')
ON CONFLICT DO NOTHING;

-- branch_manager — assign only.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'loans:collect:assign')
ON CONFLICT DO NOTHING;
