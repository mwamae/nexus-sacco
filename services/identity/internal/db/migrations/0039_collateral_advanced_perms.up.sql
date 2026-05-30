-- Phase 1.5b — collateral advanced features: four new permissions
-- for the charge / insurance / custody / auction workflows.
--
--   loans:charge_registration — record + edit legal-charge filings
--                                (Lands Registry, NTSA, etc.).
--   loans:insurance_record    — attach insurance policies + record
--                                renewals.
--   loans:custody             — check documents in/out of the vault.
--   loans:auction             — record auction events + post auction
--                                proceeds.
--
-- Role grants:
--   sacco_admin        → all four.
--   tenant_owner       → catch-all (already grants everything; restated).
--   branch_manager     → charge_registration + insurance_record (they
--                        liaise with valuers / registries / brokers).
--   credit_officer     → custody (they're the ones moving documents).
--   auctioneer-flow handlers don't get a dedicated role — sacco_admin
--   stays the only path; auctioneers themselves aren't system users.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:charge_registration', 'Record + edit legal-charge filings for collateral', 'loans'),
  ('loans:insurance_record',    'Attach + renew insurance policies on collateral',   'loans'),
  ('loans:custody',             'Check collateral documents in / out of custody',    'loans'),
  ('loans:auction',             'Record auction events + post auction proceeds',     'loans')
ON CONFLICT (code) DO NOTHING;

-- platform_admin catch-all (id …001).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code
  FROM permissions
 WHERE code IN ('loans:charge_registration','loans:insurance_record','loans:custody','loans:auction')
ON CONFLICT DO NOTHING;

-- tenant_owner catch-all (id …002).
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code
  FROM permissions
 WHERE code IN ('loans:charge_registration','loans:insurance_record','loans:custody','loans:auction')
ON CONFLICT DO NOTHING;

-- Per-role grants.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- sacco_admin (…003) — all four.
  ('00000000-0000-0000-0000-000000000003', 'loans:charge_registration'),
  ('00000000-0000-0000-0000-000000000003', 'loans:insurance_record'),
  ('00000000-0000-0000-0000-000000000003', 'loans:custody'),
  ('00000000-0000-0000-0000-000000000003', 'loans:auction'),

  -- branch_manager (…004) — charge + insurance.
  ('00000000-0000-0000-0000-000000000004', 'loans:charge_registration'),
  ('00000000-0000-0000-0000-000000000004', 'loans:insurance_record'),

  -- credit_officer (…005) — custody only.
  ('00000000-0000-0000-0000-000000000005', 'loans:custody')
ON CONFLICT (role_id, permission_code) DO NOTHING;
