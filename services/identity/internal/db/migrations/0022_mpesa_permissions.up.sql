-- Phase-6 — fine-grained M-PESA permissions.
--
-- Until now everything M-PESA-related gated on tenant:settings:edit.
-- That's coarse: customer-success roles need to run reconciliation
-- + investigate diffs but should NOT see or rotate Daraja
-- credentials. Three new permissions split the surface:
--
--   mpesa:paybill:manage     — register / disable / re-classify paybills
--   mpesa:credentials:rotate — rotate consumer key/secret/passkey/initiator
--                              creds. Higher-trust than `manage` since the
--                              plaintext crosses this UI.
--   mpesa:reconcile:run      — kick off the daily reconciliation manually,
--                              resolve diffs, see the statement-pull
--                              history. Read-only access by default.
--
-- Granted by default to existing roles based on responsibility:
--   tenant_owner:    all three (super-admin)
--   sacco_admin:     all three
--   accountant:      mpesa:reconcile:run + mpesa:paybill:manage (NOT rotate)
--   branch_manager:  mpesa:reconcile:run + mpesa:paybill:manage (NOT rotate)
--
-- credit_officer / teller / collections_officer / auditor: none.

INSERT INTO permissions (code, description, category) VALUES
  ('mpesa:paybill:manage',     'Register, disable, re-classify M-PESA paybills',          'mpesa'),
  ('mpesa:credentials:rotate', 'Rotate Daraja consumer key/secret/passkey/initiator',     'mpesa'),
  ('mpesa:reconcile:run',      'Run M-PESA reconciliation + resolve statement diffs',     'mpesa')
ON CONFLICT (code) DO NOTHING;

-- Default role grants.
INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- tenant_owner: all three
  ('00000000-0000-0000-0000-000000000002', 'mpesa:paybill:manage'),
  ('00000000-0000-0000-0000-000000000002', 'mpesa:credentials:rotate'),
  ('00000000-0000-0000-0000-000000000002', 'mpesa:reconcile:run'),
  -- sacco_admin: all three
  ('00000000-0000-0000-0000-000000000003', 'mpesa:paybill:manage'),
  ('00000000-0000-0000-0000-000000000003', 'mpesa:credentials:rotate'),
  ('00000000-0000-0000-0000-000000000003', 'mpesa:reconcile:run'),
  -- accountant: paybill + reconcile (NOT rotate creds)
  ('00000000-0000-0000-0000-000000000007', 'mpesa:paybill:manage'),
  ('00000000-0000-0000-0000-000000000007', 'mpesa:reconcile:run'),
  -- branch_manager: paybill + reconcile (NOT rotate creds)
  ('00000000-0000-0000-0000-000000000004', 'mpesa:paybill:manage'),
  ('00000000-0000-0000-0000-000000000004', 'mpesa:reconcile:run')
ON CONFLICT (role_id, permission_code) DO NOTHING;
