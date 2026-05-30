-- DSID Phase 2.2 — Standing orders + Joint accounts + Per-product recurring
-- fees + tenant-policy fields driving the standing-order processor.
--
-- The companion workflow seed (`deposit_account_reactivation` process_kind)
-- lives in services/workflow/internal/db/migrations/0013_seed_dormancy_reactivation.up.sql
-- because process_kinds are owned by the workflow service.

BEGIN;

-- ──────────── 1. Standing orders ────────────

CREATE TYPE standing_order_source AS ENUM (
  'manual_reminder',     -- system SMSes member; member acts via portal/teller
  'payroll',             -- pulled from a salary check-off batch
  'mpesa_pull',          -- STK Push initiated by system on schedule
  'fosa_debit'           -- auto-debit from member's FOSA account
);

CREATE TYPE standing_order_status AS ENUM (
  'active', 'paused', 'cancelled', 'suspended', 'completed'
);

CREATE TYPE standing_order_frequency AS ENUM (
  'weekly', 'biweekly', 'monthly', 'quarterly'
);

CREATE TABLE recurring_deposits (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id         uuid NOT NULL,
  target_account_id       uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  source                  standing_order_source NOT NULL,
  source_account_id       uuid REFERENCES deposit_accounts(id),    -- for fosa_debit
  source_msisdn           text,                                    -- for mpesa_pull (defaults to member's registered MSISDN if NULL)
  source_payroll_employer text,                                    -- for payroll (matches checkoff_batches.employer_code)
  amount                  numeric(18,2) NOT NULL CHECK (amount > 0),
  frequency               standing_order_frequency NOT NULL,
  start_date              date NOT NULL,
  end_date                date,                                    -- NULL = forever
  next_run_at             timestamptz NOT NULL,
  last_run_at             timestamptz,
  consecutive_failures    int NOT NULL DEFAULT 0,
  status                  standing_order_status NOT NULL DEFAULT 'active',
  reason_notes            text,
  last_suspended_at       timestamptz,                             -- drives the 7-day "resume needs approval" gate
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid NOT NULL,
  updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX recurring_deposits_due_idx
  ON recurring_deposits (tenant_id, next_run_at)
  WHERE status = 'active';
CREATE INDEX recurring_deposits_member_idx
  ON recurring_deposits (counterparty_id, status);

ALTER TABLE recurring_deposits ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_deposits FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_recurring_deposits ON recurring_deposits
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON recurring_deposits TO nexus_app;

-- Per-run audit (success or failure of each attempted execution).
CREATE TABLE recurring_deposit_runs (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  standing_order_id uuid NOT NULL REFERENCES recurring_deposits(id) ON DELETE CASCADE,
  attempted_at      timestamptz NOT NULL DEFAULT now(),
  amount            numeric(18,2) NOT NULL,
  attempt_no        int NOT NULL,
  period_label      text NOT NULL,                                  -- 'YYYY-MM-DD' of the due date — drives idempotency
  status            text NOT NULL CHECK (status IN ('success','failed','partial','skipped')),
  error_code        text,                                           -- 'insufficient_funds','mpesa_timeout','payroll_unmatched', ...
  error_message     text,
  posted_txn_id     uuid,
  next_retry_at     timestamptz,
  UNIQUE (standing_order_id, period_label, attempt_no)
);
CREATE INDEX recurring_deposit_runs_so_idx
  ON recurring_deposit_runs (standing_order_id, attempted_at DESC);

ALTER TABLE recurring_deposit_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE recurring_deposit_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_recurring_deposit_runs ON recurring_deposit_runs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON recurring_deposit_runs TO nexus_app;

-- ──────────── 2. Joint accounts ────────────

ALTER TABLE deposit_accounts
  ADD COLUMN IF NOT EXISTS is_joint boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS required_signers int NOT NULL DEFAULT 1
    CHECK (required_signers >= 1);

CREATE TABLE deposit_account_joint_owners (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id      uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE CASCADE,
  counterparty_id uuid NOT NULL,
  signing_role    text NOT NULL DEFAULT 'co_owner'
    CHECK (signing_role IN ('primary','co_owner','signatory')),
  added_at        timestamptz NOT NULL DEFAULT now(),
  added_by        uuid NOT NULL,
  removed_at      timestamptz,
  removed_by      uuid
);
CREATE UNIQUE INDEX deposit_account_joint_active_uniq
  ON deposit_account_joint_owners (account_id, counterparty_id)
  WHERE removed_at IS NULL;
CREATE INDEX deposit_account_joint_active_idx
  ON deposit_account_joint_owners (account_id)
  WHERE removed_at IS NULL;

ALTER TABLE deposit_account_joint_owners ENABLE ROW LEVEL SECURITY;
ALTER TABLE deposit_account_joint_owners FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_deposit_account_joint_owners ON deposit_account_joint_owners
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON deposit_account_joint_owners TO nexus_app;

-- Parent "pending withdrawal" row — the Withdraw endpoint creates one of
-- these instead of posting immediately when is_joint=true. Quorum reached
-- → status='approved' → the existing Withdraw posting path runs.
CREATE TABLE withdrawal_authorisations (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id        uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE CASCADE,
  initiated_by_counterparty_id uuid NOT NULL,
  initiated_by_user_id uuid,
  amount            numeric(18,2) NOT NULL CHECK (amount > 0),
  channel           deposit_channel NOT NULL,
  narration         text,
  required_signers  int NOT NULL CHECK (required_signers >= 1),
  status            text NOT NULL DEFAULT 'pending_joint_authorisation'
    CHECK (status IN ('pending_joint_authorisation','approved','rejected','expired','posted','cancelled')),
  expires_at        timestamptz NOT NULL,
  posted_txn_id     uuid,
  posted_at         timestamptz,
  cancellation_reason text,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX withdrawal_authorisations_account_idx
  ON withdrawal_authorisations (account_id, status);
CREATE INDEX withdrawal_authorisations_expiry_idx
  ON withdrawal_authorisations (expires_at)
  WHERE status = 'pending_joint_authorisation';

ALTER TABLE withdrawal_authorisations ENABLE ROW LEVEL SECURITY;
ALTER TABLE withdrawal_authorisations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_withdrawal_authorisations ON withdrawal_authorisations
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON withdrawal_authorisations TO nexus_app;

-- One row per active signer for each pending withdrawal. Token is the
-- SMS-clickable consent link suffix.
CREATE TABLE joint_withdrawal_authorisations (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  withdrawal_request_id    uuid NOT NULL REFERENCES withdrawal_authorisations(id) ON DELETE CASCADE,
  signer_counterparty_id   uuid NOT NULL,
  signer_msisdn            text,
  signer_token             text NOT NULL UNIQUE,                   -- public token for SMS link
  signer_status            text NOT NULL DEFAULT 'pending'
    CHECK (signer_status IN ('pending','approved','rejected')),
  responded_at             timestamptz,
  signature_method         text,                                   -- 'sms_otp','in_person','portal'
  responded_by_user_id     uuid,
  UNIQUE (withdrawal_request_id, signer_counterparty_id)
);
CREATE INDEX joint_withdrawal_auths_parent_idx
  ON joint_withdrawal_authorisations (withdrawal_request_id);

ALTER TABLE joint_withdrawal_authorisations ENABLE ROW LEVEL SECURITY;
ALTER TABLE joint_withdrawal_authorisations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_joint_withdrawal_authorisations ON joint_withdrawal_authorisations
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
-- Allow the public-token endpoint (no tenant context yet) to read by
-- token only — the policy above blocks token-bypass reads.
CREATE POLICY public_token_read_joint_withdrawal_authorisations ON joint_withdrawal_authorisations
  FOR SELECT
  USING (current_setting('app.public_token_lookup', true) = signer_token);
GRANT SELECT, INSERT, UPDATE ON joint_withdrawal_authorisations TO nexus_app;

-- ──────────── 3. Per-product recurring fees ────────────

CREATE TYPE recurring_fee_frequency AS ENUM ('monthly','quarterly','annual');

CREATE TABLE deposit_product_recurring_fees (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  product_id      uuid NOT NULL REFERENCES deposit_products(id) ON DELETE CASCADE,
  fee_kind        text NOT NULL,
  amount          numeric(18,2) NOT NULL CHECK (amount > 0),
  frequency       recurring_fee_frequency NOT NULL,
  gl_credit_code  text NOT NULL,
  active          boolean NOT NULL DEFAULT true,
  starts_on       date NOT NULL DEFAULT CURRENT_DATE,
  ends_on         date,
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid NOT NULL,
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX deposit_product_recurring_fees_product_idx
  ON deposit_product_recurring_fees (product_id, active);

ALTER TABLE deposit_product_recurring_fees ENABLE ROW LEVEL SECURITY;
ALTER TABLE deposit_product_recurring_fees FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_deposit_product_recurring_fees ON deposit_product_recurring_fees
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON deposit_product_recurring_fees TO nexus_app;

CREATE TABLE deposit_account_recurring_fee_charges (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id          uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE CASCADE,
  fee_definition_id   uuid NOT NULL REFERENCES deposit_product_recurring_fees(id) ON DELETE RESTRICT,
  period_label        text NOT NULL,
  amount              numeric(18,2) NOT NULL,
  charged_at          timestamptz NOT NULL DEFAULT now(),
  posted_txn_id       uuid,
  status              text NOT NULL CHECK (status IN ('posted','waived','insufficient_funds')),
  UNIQUE (account_id, fee_definition_id, period_label)
);
CREATE INDEX deposit_account_recurring_fee_charges_account_idx
  ON deposit_account_recurring_fee_charges (account_id, charged_at DESC);

ALTER TABLE deposit_account_recurring_fee_charges ENABLE ROW LEVEL SECURITY;
ALTER TABLE deposit_account_recurring_fee_charges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_deposit_account_recurring_fee_charges ON deposit_account_recurring_fee_charges
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON deposit_account_recurring_fee_charges TO nexus_app;

-- ──────────── 4. tenant_operations policy fields ────────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS standing_order_max_retries int NOT NULL DEFAULT 3,
  ADD COLUMN IF NOT EXISTS standing_order_retry_backoff_hours int NOT NULL DEFAULT 24,
  ADD COLUMN IF NOT EXISTS standing_order_suspend_after_failures int NOT NULL DEFAULT 3,
  ADD COLUMN IF NOT EXISTS standing_order_notify_on_failure boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS standing_order_notify_on_suspend boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS default_joint_required_signers int NOT NULL DEFAULT 2
    CHECK (default_joint_required_signers >= 1),
  ADD COLUMN IF NOT EXISTS joint_withdrawal_expiry_hours int NOT NULL DEFAULT 72;

COMMIT;
