-- Phase 6f permission: loans:writeoff. Used to gate the
-- POST /v1/loans/{loan_id}/writeoff endpoint that posts a write-off
-- ledger row, zeros the loan balances, and flips status to written_off.

INSERT INTO permissions (code, description, category) VALUES
  ('loans:writeoff', 'Authorise loan write-offs (uncollectable balances)', 'loans')
ON CONFLICT (code) DO NOTHING;

-- Re-grant catch-alls.
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000001', code FROM permissions
ON CONFLICT DO NOTHING;
INSERT INTO role_permissions (role_id, permission_code)
SELECT '00000000-0000-0000-0000-000000000002', code FROM permissions
WHERE category <> 'platform'
ON CONFLICT DO NOTHING;

-- Branch manager + accountant: write-off authority within board policy.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  ('00000000-0000-0000-0000-000000000004', 'loans:writeoff'),
  ('00000000-0000-0000-0000-000000000007', 'loans:writeoff')
ON CONFLICT DO NOTHING;
