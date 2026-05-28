-- Loans Phase 6 permissions.
--
--   loans:crb:pull          — fire a CRB query (incurs vendor cost)
--   loans:insurance:configure — manage insurance_providers
--   loans:insurance:claim   — file an insurance claim
--   members:self_service:enable — toggle members.has_self_service
--   portal:self              — member-side; granted to the member-portal
--                              role. Existing JWT carries portal vs
--                              admin context via the user's role set.
--
-- Grants:
--   platform_admin / tenant_owner — catch-alls
--   sacco_admin    — all five
--   credit_officer — loans:crb:pull (day-to-day need at scoring time)
--   accountant     — loans:insurance:configure (controls provider rates)
--   auditor        — read-only via existing loans:view; no extras

INSERT INTO permissions (code, description, category) VALUES
  ('loans:crb:pull',             'Pull a CRB report (Metropol / TransUnion / CRB Africa)', 'loans'),
  ('loans:insurance:configure',  'Configure insurance providers + rates',                  'loans'),
  ('loans:insurance:claim',      'File a credit-life insurance claim',                     'loans'),
  ('members:self_service:enable','Enable self-service portal access for a member',         'members'),
  ('portal:self',                'Self-service access — granted to the member role',       'portal')
ON CONFLICT (code) DO NOTHING;

-- Catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin: all
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'loans:crb:pull'),
  ('00000000-0000-0000-0000-000000000003', 'loans:insurance:configure'),
  ('00000000-0000-0000-0000-000000000003', 'loans:insurance:claim'),
  ('00000000-0000-0000-0000-000000000003', 'members:self_service:enable')
ON CONFLICT DO NOTHING;

-- credit_officer: CRB pull
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000005', 'loans:crb:pull')
ON CONFLICT DO NOTHING;

-- accountant: insurance configure
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'loans:insurance:configure')
ON CONFLICT DO NOTHING;

-- The member role (...00000a) gets portal:self so the member-portal
-- JWT authorisation works out of the box.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-00000000000a', 'portal:self')
ON CONFLICT DO NOTHING;
