-- Phase 1.5a — Collateral foundation: three new permissions for the
-- collateral lifecycle + the approval-gate override.
--
--   loans:verify_collateral   — flips offered → verified (with notes
--                                + inspection photos). Held by credit
--                                staff who physically inspect.
--   loans:value_collateral    — attaches a valuation (market + FSV +
--                                report PDF). Held by valuation desk
--                                / panel-valuer liaison.
--   loans:override_coverage   — bypass the security-coverage approval
--                                gate. Use sparingly; writes a
--                                permanent loan_coverage_overrides
--                                audit row.
--
-- Role grants follow the prompt §4:
--
--   sacco_admin   → all three (verify, value, override)
--   tenant_owner  → catch-all (already grants everything via
--                   0002_seed_rbac; restated for clarity)
--   branch_manager + credit_officer → verify_collateral
--                  (they're already in the field; valuation typically
--                  goes through an external panel, so we don't grant
--                  value_collateral by default — opt-in per-tenant).
--   override_coverage stays tight: tenant_owner + sacco_admin only.
--
-- Idempotent: ON CONFLICT DO NOTHING throughout.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:verify_collateral', 'Inspect + verify offered collateral items', 'loans'),
  ('loans:value_collateral',  'Attach a market + FSV valuation to a collateral item', 'loans'),
  ('loans:override_coverage', 'Bypass the security-coverage approval gate (audited)', 'loans')
ON CONFLICT (code) DO NOTHING;

-- platform_admin catch-all (id …001).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code
  FROM permissions
 WHERE code IN ('loans:verify_collateral', 'loans:value_collateral', 'loans:override_coverage')
ON CONFLICT DO NOTHING;

-- tenant_owner catch-all (id …002).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code
  FROM permissions
 WHERE code IN ('loans:verify_collateral', 'loans:value_collateral', 'loans:override_coverage')
ON CONFLICT DO NOTHING;

-- Explicit per-role grants.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- sacco_admin (…003) — all three.
  ('00000000-0000-0000-0000-000000000003', 'loans:verify_collateral'),
  ('00000000-0000-0000-0000-000000000003', 'loans:value_collateral'),
  ('00000000-0000-0000-0000-000000000003', 'loans:override_coverage'),

  -- branch_manager (…004) — verify only (valuation outsourced; override stays tight).
  ('00000000-0000-0000-0000-000000000004', 'loans:verify_collateral'),

  -- credit_officer (…005) — verify only.
  ('00000000-0000-0000-0000-000000000005', 'loans:verify_collateral')
ON CONFLICT (role_id, permission_code) DO NOTHING;
