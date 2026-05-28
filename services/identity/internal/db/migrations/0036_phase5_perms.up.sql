-- Loans Phase 5 permissions.
--
-- loans:topup            — create top-up applications
-- loans:refinance        — create refinance applications
-- loans:checkoff:upload  — upload + validate a check-off batch
-- loans:checkoff:post    — post a validated check-off batch (creates JEs)
-- loans:view:insider     — see insider loans in lists + reports
--
-- Grant matrix:
--   platform_admin + tenant_owner — catch-all
--   sacco_admin    — all five (including the audit-visible insider view)
--   credit_officer — topup + refinance (the day-to-day flows)
--   accountant     — checkoff:upload + checkoff:post (operations)
--   auditor        — view:insider (read-only governance)

INSERT INTO permissions (code, description, category) VALUES
  ('loans:topup',           'Create top-up loan applications',                     'loans'),
  ('loans:refinance',       'Create refinance (incl. consolidation) applications', 'loans'),
  ('loans:checkoff:upload', 'Upload + validate salary check-off batches',           'loans'),
  ('loans:checkoff:post',   'Post a validated check-off batch to the ledger',       'loans'),
  ('loans:view:insider',    'See insider-flagged loans in lists + reports',         'loans')
ON CONFLICT (code) DO NOTHING;

-- Catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- sacco_admin: all five.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000003', 'loans:topup'),
  ('00000000-0000-0000-0000-000000000003', 'loans:refinance'),
  ('00000000-0000-0000-0000-000000000003', 'loans:checkoff:upload'),
  ('00000000-0000-0000-0000-000000000003', 'loans:checkoff:post'),
  ('00000000-0000-0000-0000-000000000003', 'loans:view:insider')
ON CONFLICT DO NOTHING;

-- credit_officer: topup + refinance.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000005', 'loans:topup'),
  ('00000000-0000-0000-0000-000000000005', 'loans:refinance')
ON CONFLICT DO NOTHING;

-- accountant: checkoff upload + post.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000007', 'loans:checkoff:upload'),
  ('00000000-0000-0000-0000-000000000007', 'loans:checkoff:post')
ON CONFLICT DO NOTHING;

-- auditor: read-only insider view.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000008', 'loans:view:insider')
ON CONFLICT DO NOTHING;
