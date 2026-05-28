-- Loans Phase 3 — provisioning + loans-policy permissions.
--
-- Three new perms layered on top of the loans:reports gate seeded by
-- Phase 1 (migration 0032) and the loans:sasra gate from Phase 2
-- (migration 0033):
--
--   loans:provisioning:run   — create / compute / cancel a draft
--                              provisioning run. Granted to the
--                              accountant (owns the monthly cycle)
--                              and sacco_admin (oversight).
--
--   loans:provisioning:post  — post the run's JE (DR 5210 Loan Loss
--                              Provisioning Expense / CR 1120 Loan
--                              Loss Provision). Same grant set as
--                              :run — IFRS 9 governance can tighten
--                              this later by revoking :post from the
--                              accountant and keeping :run open.
--
--   loans:policy:write       — edit tenant_operations DPD thresholds
--                              (sasra_watch_dpd, dpd_substandard_days,
--                              dpd_doubtful_days, dpd_loss_days,
--                              ifrs9_stage2_dpd, ifrs9_stage3_dpd)
--                              and the ecl_rate_matrix. Tightly held —
--                              tenant_owner + sacco_admin only. The
--                              accountant CANNOT change the rates that
--                              produce the JE they're about to post —
--                              segregation of duties.
--
-- Catch-alls (platform_admin + tenant_owner) get the new perms via the
-- standard wildcard insert below.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:provisioning:run',  'Create / compute / cancel a loan provisioning run', 'loans'),
  ('loans:provisioning:post', 'Post the journal entry for a computed provisioning run', 'loans'),
  ('loans:policy:write',      'Edit DPD thresholds and ECL rate matrix',           'loans')
ON CONFLICT (code) DO NOTHING;

-- Catch-alls: platform_admin gets everything; tenant_owner everything
-- in non-platform categories.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin — full provisioning lifecycle + policy editing.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'loans:provisioning:run'),
  ('00000000-0000-0000-0000-000000000003', 'loans:provisioning:post'),
  ('00000000-0000-0000-0000-000000000003', 'loans:policy:write')
ON CONFLICT DO NOTHING;

-- accountant — runs the monthly cycle and posts the JE, but cannot
-- change the rates that drive the calculation.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'loans:provisioning:run'),
  ('00000000-0000-0000-0000-000000000007', 'loans:provisioning:post')
ON CONFLICT DO NOTHING;

-- auditor — read-only via existing loans:reports + loans:sasra. No
-- new explicit grants; the new perms are write-only.
