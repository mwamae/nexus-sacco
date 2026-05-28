-- Loans Phase 2 — reporting + SASRA permissions.
--
-- Two new perms layered on top of the loans:reports perm seeded by
-- Phase 1 (migration 0032):
--
--   loans:sasra          — generate the SASRA quarterly extract.
--                          Sensitive: granted to sacco_admin +
--                          auditor only. The CSV is what the SACCO
--                          uploads to the regulator's portal, so we
--                          gate it more tightly than the dashboards.
--
--   loans:reports:export — download the per-tab CSVs from the
--                          Reports page. Same role set as
--                          loans:reports — if you can see the
--                          numbers on screen, you can export them.
--                          Separating the gate makes it possible
--                          to revoke just the export capability
--                          later (e.g. compliance hold on a
--                          specific role) without losing the
--                          dashboards.
--
-- Catch-alls (platform_admin + tenant_owner) get the new perms
-- automatically per the project's seed-migration pattern.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:sasra',          'Generate the SASRA quarterly extract',     'loans'),
  ('loans:reports:export', 'Download CSVs from the Reports page',      'loans')
ON CONFLICT (code) DO NOTHING;

-- platform_admin + tenant_owner catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code
  FROM permissions
 WHERE code IN ('loans:sasra', 'loans:reports:export')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code
  FROM permissions
 WHERE code IN ('loans:sasra', 'loans:reports:export')
ON CONFLICT DO NOTHING;

-- Per-role explicit grants.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- sacco_admin (id …003) — full reporting + SASRA.
  ('00000000-0000-0000-0000-000000000003', 'loans:sasra'),
  ('00000000-0000-0000-0000-000000000003', 'loans:reports:export'),

  -- branch_manager (id …004) — export but no SASRA.
  ('00000000-0000-0000-0000-000000000004', 'loans:reports:export'),

  -- auditor (id …008) — SASRA + export (auditor is the one verifying
  -- the extract against ledger reconciliation; they need both surfaces).
  ('00000000-0000-0000-0000-000000000008', 'loans:sasra'),
  ('00000000-0000-0000-0000-000000000008', 'loans:reports:export')
ON CONFLICT (role_id, permission_code) DO NOTHING;
