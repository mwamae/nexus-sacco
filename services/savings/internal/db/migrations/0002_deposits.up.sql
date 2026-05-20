-- ═══════════════════════════════════════════════════════════════════
-- SAVINGS service — Deposits sub-module.
--
-- Deposits represent member SAVINGS (liability on the SACCO balance
-- sheet). They are not equity — those are tracked in 0001 (shares).
--
-- Tables created here:
--   • deposit_products       — tenant-configurable product catalog
--                              (7 product types: ordinary, fixed, junior,
--                              holiday, goal, emergency, group)
--   • deposit_accounts       — per (member, product) pair; carries the
--                              cached running balance + product-specific
--                              metadata (goal target, guardian, lock-in
--                              maturity, etc.)
--   • deposit_transactions   — append-only signed ledger. + for credits
--                              (deposit, interest, transfer_in, adjust+),
--                              − for debits (withdrawal, fee, transfer_out,
--                              adjust−). Each row carries balance_after.
--   • deposit_daily_balances — end-of-day snapshot per account. Built
--                              by the snapshot job; consumed by the
--                              Phase 4 interest engine.
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Enums ───────────

CREATE TYPE deposit_product_type AS ENUM (
  'ordinary',    -- voluntary savings, freely deposit/withdraw
  'fixed',       -- locked tenure, fixed rate, early-withdrawal penalty
  'junior',      -- minor account, linked to a guardian member
  'holiday',     -- contributions year-round, withdrawals in a window
  'goal',        -- target amount + date; restricted withdrawals
  'emergency',   -- restricted; withdrawal requires evidence + approval
  'group'        -- chama / pooled group account
);

CREATE TYPE deposit_eligibility AS ENUM (
  'individuals', 'groups', 'minors', 'all'
);

CREATE TYPE deposit_maturity_action AS ENUM (
  'none', 'auto_renew', 'liquidate_to_ordinary', 'notify'
);

CREATE TYPE deposit_fee_frequency AS ENUM (
  'none', 'monthly', 'quarterly', 'annual'
);

CREATE TYPE deposit_account_status AS ENUM (
  'pending', 'active', 'dormant', 'suspended', 'matured', 'closed'
);

CREATE TYPE deposit_txn_type AS ENUM (
  'opening_balance',
  'deposit',
  'withdrawal',
  'transfer_in',
  'transfer_out',
  'interest_credit',
  'fee_debit',
  'reversal',
  'adjustment',
  'goal_payout'
);

CREATE TYPE deposit_channel AS ENUM (
  'cash',
  'mpesa',
  'airtel_money',
  'bank_transfer',
  'standing_order',
  'direct_debit',
  'payroll',
  'internal'      -- transfers, interest postings, fees, reversals, adjustments
);

-- ─────────── deposit_products ───────────
-- Tenant-configurable product catalog. All numeric thresholds are in
-- the tenant currency. NULL means "no constraint" — e.g. max_balance
-- NULL means uncapped.
CREATE TABLE deposit_products (
  id                              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code                            text NOT NULL,
  name                            text NOT NULL,
  product_type                    deposit_product_type NOT NULL,
  description                     text,
  is_active                       boolean NOT NULL DEFAULT true,

  -- Balance / per-txn rules
  min_opening_balance             numeric(18,2) NOT NULL DEFAULT 0,
  min_operating_balance           numeric(18,2) NOT NULL DEFAULT 0,
  max_balance                     numeric(18,2),                      -- NULL = uncapped
  min_deposit_amount              numeric(18,2) NOT NULL DEFAULT 0,
  max_deposit_amount              numeric(18,2),                      -- NULL = uncapped
  min_withdrawal_amount           numeric(18,2) NOT NULL DEFAULT 0,
  max_withdrawal_amount           numeric(18,2),                      -- NULL = uncapped

  -- Withdrawal rules
  notice_period_days              int NOT NULL DEFAULT 0,             -- 0 = on-demand
  max_withdrawals_per_month       int,                                 -- NULL = unlimited
  partial_withdrawal_allowed      boolean NOT NULL DEFAULT true,
  large_withdrawal_threshold      numeric(18,2),                      -- > triggers workflow (Phase 4)

  -- Lock-in / maturity (fixed deposits)
  lock_in_months                  int NOT NULL DEFAULT 0,
  default_term_months             int,                                 -- for fixed deposits
  maturity_action                 deposit_maturity_action NOT NULL DEFAULT 'none',

  -- Eligibility
  eligibility                     deposit_eligibility NOT NULL DEFAULT 'individuals',
  requires_approval_to_open       boolean NOT NULL DEFAULT false,

  -- Withdrawal-window products (e.g. holiday: Nov–Dec)
  -- Months 1-12; NULL means no window restriction.
  withdrawal_window_start_month   int CHECK (withdrawal_window_start_month BETWEEN 1 AND 12),
  withdrawal_window_end_month     int CHECK (withdrawal_window_end_month   BETWEEN 1 AND 12),

  -- Charges
  maintenance_fee                 numeric(18,2) NOT NULL DEFAULT 0,
  maintenance_fee_frequency       deposit_fee_frequency NOT NULL DEFAULT 'none',
  early_withdrawal_penalty_pct    numeric(6,3)  NOT NULL DEFAULT 0,    -- % of withdrawn amount
  below_min_balance_fee           numeric(18,2) NOT NULL DEFAULT 0,
  dormancy_fee_monthly            numeric(18,2) NOT NULL DEFAULT 0,

  created_at                      timestamptz NOT NULL DEFAULT now(),
  updated_at                      timestamptz NOT NULL DEFAULT now(),
  created_by                      uuid,
  UNIQUE (tenant_id, code)
);
CREATE INDEX deposit_products_tenant_active_idx
  ON deposit_products (tenant_id, is_active, product_type);
CREATE TRIGGER deposit_products_updated_at BEFORE UPDATE ON deposit_products
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE deposit_products IS
  'Tenant-configurable deposit product catalog. Every deposit_account binds to one product.';

-- ─────────── deposit_accounts ───────────
-- One per (member, product) combo. A member can hold many deposit
-- accounts across different products (e.g. ordinary + a fixed deposit +
-- a goal-savings target). Group accounts use a member_id pointer to the
-- group's representative organisation-member record.
CREATE TABLE deposit_accounts (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_id                   uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  product_id                  uuid NOT NULL REFERENCES deposit_products(id) ON DELETE RESTRICT,
  account_no                  text NOT NULL,
  status                      deposit_account_status NOT NULL DEFAULT 'pending',

  -- Cached balances (the ledger is source of truth; we cache for fast
  -- reads + atomic CAS during posting).
  current_balance             numeric(18,2) NOT NULL DEFAULT 0,
  available_balance           numeric(18,2) NOT NULL DEFAULT 0,   -- minus pending holds

  -- Lifecycle
  opened_at                   timestamptz,
  matures_at                  timestamptz,
  closed_at                   timestamptz,
  last_activity_at            timestamptz,
  last_deposit_at             timestamptz,
  last_withdrawal_at          timestamptz,

  -- Fixed deposit specifics (snapshot at account open time)
  fixed_term_months           int,
  fixed_interest_rate_pct     numeric(6,3),

  -- Goal savings
  goal_target_amount          numeric(18,2),
  goal_target_date            date,
  goal_description            text,

  -- Junior accounts: link to the parent/guardian member
  guardian_member_id          uuid REFERENCES members(id) ON DELETE RESTRICT,

  -- Group accounts: external group reference (org_members.id from the
  -- member service). Not FKd because RLS-isolated and cross-service.
  group_org_id                uuid,

  -- Notice-period tracking (for products with notice_period_days > 0):
  -- the member declares intent to withdraw a specific amount; once
  -- elapsed they can post the withdrawal.
  withdrawal_notice_given_at  timestamptz,
  withdrawal_notice_amount    numeric(18,2),

  created_at                  timestamptz NOT NULL DEFAULT now(),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid,
  UNIQUE (tenant_id, account_no)
);
CREATE INDEX deposit_accounts_member_idx ON deposit_accounts (member_id);
CREATE INDEX deposit_accounts_product_idx ON deposit_accounts (product_id);
CREATE INDEX deposit_accounts_tenant_status_idx ON deposit_accounts (tenant_id, status);
CREATE INDEX deposit_accounts_matures_idx ON deposit_accounts (tenant_id, matures_at)
  WHERE matures_at IS NOT NULL;
CREATE INDEX deposit_accounts_last_activity_idx ON deposit_accounts (tenant_id, last_activity_at)
  WHERE status = 'active';
CREATE TRIGGER deposit_accounts_updated_at BEFORE UPDATE ON deposit_accounts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A member may hold multiple accounts under the same product (e.g.
-- multiple goal-savings buckets), so no UNIQUE (member, product). But
-- for ordinary savings, application logic restricts to one. The store
-- enforces that.

-- ─────────── deposit_transactions ───────────
-- Append-only signed ledger. amount is the signed money movement:
--   deposit / transfer_in / interest_credit / adjustment(+) → positive
--   withdrawal / transfer_out / fee_debit / adjustment(−) → negative
--   reversal copies the inverse of the reversed row.
-- balance_after is the running cached balance after posting this row.
CREATE TABLE deposit_transactions (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id               uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  member_id                uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  txn_no                   text NOT NULL,
  txn_type                 deposit_txn_type NOT NULL,
  amount                   numeric(18,2) NOT NULL CHECK (amount <> 0),
  value_date               date NOT NULL DEFAULT CURRENT_DATE,
  channel                  deposit_channel,
  channel_ref              text,
  narration                text,

  -- For transfers
  counterparty_account_id  uuid REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  counterparty_txn_id      uuid REFERENCES deposit_transactions(id) ON DELETE SET NULL,

  -- For reversals
  reverses_txn_id          uuid REFERENCES deposit_transactions(id) ON DELETE RESTRICT,
  reversed_by_txn_id       uuid REFERENCES deposit_transactions(id) ON DELETE SET NULL,
  reversal_reason          text,

  -- Balance after this row (cached for fast statement reads)
  balance_after            numeric(18,2) NOT NULL,

  initiated_by             uuid NOT NULL,
  authorized_by            uuid,
  authorization_reason     text,

  -- Workflow integration (Phase 4 large-withdrawal gating)
  workflow_instance_id     uuid,

  posted_at                timestamptz NOT NULL DEFAULT now(),
  created_at               timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, txn_no)
);
CREATE INDEX deposit_txn_account_posted_idx
  ON deposit_transactions (account_id, posted_at DESC);
CREATE INDEX deposit_txn_member_posted_idx
  ON deposit_transactions (member_id, posted_at DESC);
CREATE INDEX deposit_txn_tenant_type_idx
  ON deposit_transactions (tenant_id, txn_type, posted_at DESC);
CREATE INDEX deposit_txn_value_date_idx
  ON deposit_transactions (tenant_id, value_date);
CREATE INDEX deposit_txn_channel_ref_idx
  ON deposit_transactions (tenant_id, channel, channel_ref)
  WHERE channel_ref IS NOT NULL;

-- ─────────── deposit_daily_balances ───────────
-- End-of-day balance snapshot per account. The snapshot job upserts a
-- row per day per active account. The Phase 4 interest engine uses
-- these as inputs to the weighted-average computation.
CREATE TABLE deposit_daily_balances (
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id    uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE CASCADE,
  snapshot_date date NOT NULL,
  balance       numeric(18,2) NOT NULL,
  product_id    uuid NOT NULL,
  member_id     uuid NOT NULL,
  PRIMARY KEY (account_id, snapshot_date)
);
CREATE INDEX deposit_daily_balances_tenant_date_idx
  ON deposit_daily_balances (tenant_id, snapshot_date);
CREATE INDEX deposit_daily_balances_product_date_idx
  ON deposit_daily_balances (tenant_id, product_id, snapshot_date);

-- Per-tenant sequence kinds extend the existing share_number_seq table
-- from 0001 (tenant_id, kind, year, last_value). New kinds:
--   'deposit_account' → DPA-YYYY-NNNNN
--   'deposit_txn'     → DPT-YYYY-NNNNN
-- No schema change needed; the store just uses new kind strings.

-- ─────────── RLS ───────────
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'deposit_products', 'deposit_accounts',
    'deposit_transactions', 'deposit_daily_balances'
  ])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

-- ─────────── Grants ───────────
GRANT SELECT, INSERT, UPDATE, DELETE ON
  deposit_products, deposit_accounts,
  deposit_transactions, deposit_daily_balances
TO nexus_app;
