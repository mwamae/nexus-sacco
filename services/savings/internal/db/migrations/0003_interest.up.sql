-- ═══════════════════════════════════════════════════════════════════
-- SAVINGS service — Interest engine + WHT.
--
-- Deposit interest in a SACCO is fundamentally different from bank
-- interest. It's declared ONCE per financial year, AFTER the books
-- close, at a rate APPROVED AT THE AGM. The system accumulates daily
-- balances (deposit_daily_balances from 0002) all year, then runs the
-- weighted-average computation when the AGM rate is entered.
--
-- Tables created here:
--   • interest_runs        — header per FY interest declaration:
--                            AGM ref, rate, FY range, status, totals
--   • interest_run_lines   — per-member-per-account computation:
--                            weighted average, gross interest, WHT, net,
--                            payout method, posted txn id
--   • tax_payable_ledger   — running WHT remittance ledger (KRA)
--
-- Tenant configuration additions:
--   • fy_start_month / fy_start_day  — FY start (e.g. 1/1 for Jan or 7/1 for July)
--   • default_interest_payout        — credit_savings | buy_shares | external
--
-- Product configuration addition:
--   • interest_eligible              — included in the variable-rate run?
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Tenant + product config extensions ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS fy_start_month            int NOT NULL DEFAULT 1
    CHECK (fy_start_month BETWEEN 1 AND 12),
  ADD COLUMN IF NOT EXISTS fy_start_day              int NOT NULL DEFAULT 1
    CHECK (fy_start_day BETWEEN 1 AND 31),
  ADD COLUMN IF NOT EXISTS default_interest_payout   text NOT NULL DEFAULT 'credit_savings'
    CHECK (default_interest_payout IN ('credit_savings', 'buy_shares', 'external'));

ALTER TABLE deposit_products
  ADD COLUMN IF NOT EXISTS interest_eligible boolean NOT NULL DEFAULT true;

COMMENT ON COLUMN tenant_operations.fy_start_month IS
  'Financial-year start month (1-12). E.g. 1 = January (Jan-Dec FY).';
COMMENT ON COLUMN deposit_products.interest_eligible IS
  'When true, deposits in this product participate in the AGM-rate interest run. Fixed deposits with their own contractual rate typically opt out.';

-- ─────────── Enums ───────────

CREATE TYPE interest_run_status AS ENUM (
  'draft',       -- created; rate/AGM ref captured; not yet computed
  'computing',   -- compute job running (transient)
  'preview',     -- lines generated, awaiting authorization
  'approved',    -- approval received (workflow or direct authorisation)
  'posting',     -- posting in progress (transient)
  'posted',      -- all lines credited; tax_payable rows written
  'locked',      -- frozen; cannot be reversed without a formal adjustment
  'cancelled'    -- abandoned before posting
);

CREATE TYPE interest_payout_method AS ENUM (
  'credit_savings',  -- net interest credited to a designated savings account
  'buy_shares',      -- net interest used to purchase additional shares
  'external'         -- net interest paid out via M-Pesa / bank
);

-- ─────────── interest_runs ───────────

CREATE TABLE interest_runs (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_no                      text NOT NULL,                  -- e.g. IR-2026-00001
  financial_year_label        text NOT NULL,                  -- e.g. "FY 2025-2026"
  fy_start                    date NOT NULL,
  fy_end                      date NOT NULL,                  -- inclusive
  status                      interest_run_status NOT NULL DEFAULT 'draft',

  -- AGM authorisation (required before any progression past 'draft')
  agm_rate_pct                numeric(6,3) NOT NULL,          -- e.g. 8.500 = 8.5% p.a.
  agm_resolution_ref          text NOT NULL,
  agm_resolution_date         date NOT NULL,
  wht_rate_pct                numeric(5,2) NOT NULL,          -- snapshot at run-creation time

  -- In-scope product selection
  product_ids                 uuid[] NOT NULL,                -- subset of interest_eligible products

  -- Aggregates (filled after compute)
  member_count                int,
  total_weighted_balance      numeric(20,2),
  total_gross_interest        numeric(18,2),
  total_wht                   numeric(18,2),
  total_net_interest          numeric(18,2),

  notes                       text,
  created_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid NOT NULL,
  computed_at                 timestamptz,
  computed_by                 uuid,
  submitted_at                timestamptz,
  submitted_by                uuid,
  workflow_instance_id        uuid,
  approved_at                 timestamptz,
  approved_by                 uuid,
  posted_at                   timestamptz,
  posted_by                   uuid,
  locked_at                   timestamptz,
  cancelled_at                timestamptz,
  cancelled_by                uuid,
  cancellation_reason         text,
  UNIQUE (tenant_id, run_no)
);
CREATE INDEX interest_runs_tenant_status_idx ON interest_runs (tenant_id, status, fy_end DESC);
CREATE INDEX interest_runs_fy_idx ON interest_runs (tenant_id, fy_start, fy_end);

COMMENT ON TABLE interest_runs IS
  'One row per FY interest declaration. Status flows draft → preview → approved → posted → locked.';

-- ─────────── interest_run_lines ───────────
-- One row per (member, account) participating in the run. Per the spec,
-- a member may have multiple accounts; each gets its own line so the
-- audit trail and statement-level allocation are clean.

CREATE TABLE interest_run_lines (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id                  uuid NOT NULL REFERENCES interest_runs(id) ON DELETE CASCADE,
  account_id              uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  member_id               uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  product_id              uuid NOT NULL,

  -- Computation inputs
  days_in_fy              int NOT NULL,
  days_with_snapshots     int NOT NULL,             -- coverage check
  sum_of_daily_balances   numeric(20,2) NOT NULL,   -- Σ(balance × 1 day)
  weighted_avg_balance    numeric(20,2) NOT NULL,   -- sum / days_in_fy
  rate_applied_pct        numeric(6,3) NOT NULL,
  wht_rate_pct            numeric(5,2) NOT NULL,

  -- Computation outputs
  gross_interest          numeric(18,2) NOT NULL,
  wht_amount              numeric(18,2) NOT NULL,
  net_interest            numeric(18,2) NOT NULL,

  -- Payout — defaults to tenant default at compute time; settable per line
  payout_method           interest_payout_method NOT NULL DEFAULT 'credit_savings',
  payout_target_account_id uuid,                    -- credit_savings → which deposit account
  payout_external_channel text,                     -- 'mpesa' | 'bank_transfer' | ...
  payout_external_ref     text,

  -- Posting outputs (filled after run → 'posted')
  posted_at               timestamptz,
  posted_txn_id           uuid,                     -- deposit_transactions row created at posting
  share_txn_id            uuid,                     -- share_transactions row, if buy_shares path

  notes                   text,
  UNIQUE (run_id, account_id)
);
CREATE INDEX interest_run_lines_run_idx ON interest_run_lines (run_id);
CREATE INDEX interest_run_lines_member_idx ON interest_run_lines (member_id, run_id);

COMMENT ON COLUMN interest_run_lines.sum_of_daily_balances IS
  'Σ(balance × 1 day) across the FY — built from deposit_daily_balances snapshots.';

-- ─────────── tax_payable_ledger ───────────
-- Running ledger of WHT collected, broken down by member/run for clean
-- per-member tax certificates AND aggregate KRA remittance reports.

CREATE TABLE tax_payable_ledger (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source_kind     text NOT NULL,                    -- 'interest_run' | 'dividend_run' | 'manual'
  source_id       uuid,                             -- references interest_runs.id, etc.
  member_id       uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  member_no       text NOT NULL,                    -- denormalised for fast reporting
  member_name     text NOT NULL,
  fy_label        text NOT NULL,
  gross_amount    numeric(18,2) NOT NULL,           -- pre-tax (e.g. gross interest)
  wht_rate_pct    numeric(5,2) NOT NULL,
  wht_amount      numeric(18,2) NOT NULL,
  posted_at       timestamptz NOT NULL DEFAULT now(),
  posted_by       uuid NOT NULL,
  remitted_at     timestamptz,                      -- when the SACCO paid this to the tax authority
  remittance_ref  text
);
CREATE INDEX tax_payable_tenant_fy_idx ON tax_payable_ledger (tenant_id, fy_label, posted_at);
CREATE INDEX tax_payable_member_idx ON tax_payable_ledger (member_id, posted_at DESC);
CREATE INDEX tax_payable_source_idx ON tax_payable_ledger (source_kind, source_id);

-- ─────────── RLS ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['interest_runs', 'interest_run_lines', 'tax_payable_ledger'])
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
  interest_runs, interest_run_lines, tax_payable_ledger
TO nexus_app;
