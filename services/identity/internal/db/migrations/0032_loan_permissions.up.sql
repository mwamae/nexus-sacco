-- Loans Phase 1 — two missing permissions for the new section gating.
--
-- The existing loans:* family (loans:view, loans:apply, loans:approve,
-- loans:assess, loans:configure, loans:disburse, loans:guarantee,
-- loans:offer, loans:originate, loans:restructure, loans:reverse,
-- loans:underwrite, loans:writeoff) was seeded in earlier migrations
-- and is already granted to the relevant roles. Phase 1 introduces
-- two new gate keys the new Loans → {Collections, Reports, Provisioning}
-- nav entries use:
--
--   loans:collect — gates the Collections subsection. Phase 4 fills
--                   the actual case-work UI; Phase 1 just shows the
--                   placeholder route to anyone who'd later use it.
--
--   loans:reports — gates the Reports + Provisioning subsections.
--                   Auditors need read-only access without picking
--                   up the broader loans:view (which grants drill-in
--                   to the register; reports stay summary-only).
--
-- Role grants follow the Phase 1 prompt:
--
--   branch_manager   → loans:view + loans:apply + loans:collect + loans:reports
--   credit_officer   → loans:view + loans:apply + loans:collect
--   sacco_admin      → all loans:* (picks up the new perms via the
--                      catch-all grant pattern in 0002_seed_rbac.up.sql
--                      that the migration re-applies for new perms; we
--                      restate the explicit grant here for clarity)
--   auditor          → loans:view + loans:reports
--
-- Idempotent: ON CONFLICT DO NOTHING on both the perm insert + the
-- role-permission grants, so re-running the migration is a no-op.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:collect', 'Work cases in the Loans → Collections section', 'loans'),
  ('loans:reports', 'View loan reports + provisioning dashboards', 'loans')
ON CONFLICT (code) DO NOTHING;

-- Re-apply the platform_admin catch-all so the new perms are visible
-- to super-admins without a manual grant. Mirrors the pattern used by
-- every loans:* permission addition (see 0010_deposits_perms,
-- 0012_loan_perms).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code
  FROM permissions
 WHERE code IN ('loans:collect', 'loans:reports')
ON CONFLICT DO NOTHING;

-- Re-apply tenant_owner catch-all (everything except platform:*).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code
  FROM permissions
 WHERE code IN ('loans:collect', 'loans:reports')
ON CONFLICT DO NOTHING;

-- Per-role explicit grants per the Phase 1 prompt.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- branch_manager (id …004): view + apply already granted in 0012;
  -- new perms collect + reports.
  ('00000000-0000-0000-0000-000000000004', 'loans:collect'),
  ('00000000-0000-0000-0000-000000000004', 'loans:reports'),

  -- credit_officer (id …005): view + apply already granted; collect new.
  -- (No reports — credit officers act on individual cases, not aggregates.)
  ('00000000-0000-0000-0000-000000000005', 'loans:collect'),

  -- sacco_admin (id …003): full loans:*. configure already granted;
  -- collect + reports new.
  ('00000000-0000-0000-0000-000000000003', 'loans:collect'),
  ('00000000-0000-0000-0000-000000000003', 'loans:reports'),

  -- auditor (id …008): view already granted in 0012; reports new.
  -- Read-only — no collect.
  ('00000000-0000-0000-0000-000000000008', 'loans:reports')
ON CONFLICT (role_id, permission_code) DO NOTHING;
