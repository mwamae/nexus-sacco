-- ═══════════════════════════════════════════════════════════════════
-- SAVINGS service — Dividends engine.
--
-- Dividends are paid out of SACCO surplus on member SHARE balances,
-- not deposit balances. Governance mirrors the interest engine:
-- AGM-gated rate, FY-scoped, preview → approval → posting → lock.
--
-- Three calculation methods supported:
--   closing_balance  — share balance at FY-end (simplest)
--   average_monthly  — average of 12 month-end balances over the FY
--   pro_rated        — closing × (days_held_in_fy / days_in_fy),
--                      for members who joined or exited mid-year
--
-- Per-line outcomes use the existing share_transactions /
-- deposit_transactions / tax_payable_ledger ledgers. No new ledger
-- tables — just the run + lines audit trail.
-- ═══════════════════════════════════════════════════════════════════

CREATE TYPE dividend_run_status AS ENUM (
  'draft', 'computing', 'preview', 'approved',
  'posting', 'posted', 'locked', 'cancelled'
);

CREATE TYPE dividend_calc_method AS ENUM (
  'closing_balance',
  'average_monthly',
  'pro_rated'
);

-- ─────────── dividend_runs ───────────

CREATE TABLE dividend_runs (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_no                      text NOT NULL,                  -- DV-2026-00001
  financial_year_label        text NOT NULL,
  fy_start                    date NOT NULL,
  fy_end                      date NOT NULL,
  status                      dividend_run_status NOT NULL DEFAULT 'draft',
  calc_method                 dividend_calc_method NOT NULL DEFAULT 'closing_balance',

  -- AGM authorisation — required for any progression past 'draft'.
  agm_rate_pct                numeric(6,3) NOT NULL,          -- e.g. 12.500 = 12.5% on the share basis
  agm_resolution_ref          text NOT NULL,
  agm_resolution_date         date NOT NULL,
  wht_rate_pct                numeric(5,2) NOT NULL,

  -- Aggregates (filled after compute)
  member_count                int,
  total_share_basis           numeric(20,2),                  -- sum of per-member basis
  total_gross_dividend        numeric(18,2),
  total_wht                   numeric(18,2),
  total_net_dividend          numeric(18,2),

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
CREATE INDEX dividend_runs_tenant_status_idx ON dividend_runs (tenant_id, status, fy_end DESC);
CREATE INDEX dividend_runs_fy_idx ON dividend_runs (tenant_id, fy_start, fy_end);

COMMENT ON TABLE dividend_runs IS
  'One row per FY dividend declaration. Status flows draft → preview → approved → posted → locked.';

-- ─────────── dividend_run_lines ───────────
-- One row per (member, share_account). Captures the balance basis used
-- (depending on calc method) and the gross/WHT/net split.

CREATE TABLE dividend_run_lines (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id                  uuid NOT NULL REFERENCES dividend_runs(id) ON DELETE CASCADE,
  share_account_id        uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  member_id               uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,

  -- Computation inputs / context
  calc_method             dividend_calc_method NOT NULL,
  shares_basis            numeric(20,2) NOT NULL,           -- the share count (or weighted avg) used
  par_value_at_run        numeric(18,2) NOT NULL,           -- par at compute time, for traceability
  capital_basis           numeric(20,2) NOT NULL,           -- shares_basis × par_value_at_run
  days_held_in_fy         int,                              -- for pro_rated; null otherwise
  days_in_fy              int NOT NULL,
  rate_applied_pct        numeric(6,3) NOT NULL,
  wht_rate_pct            numeric(5,2) NOT NULL,

  -- Outputs
  gross_dividend          numeric(18,2) NOT NULL,
  wht_amount              numeric(18,2) NOT NULL,
  net_dividend            numeric(18,2) NOT NULL,

  -- Payout — reuses the interest_payout_method enum (defined in
  -- migration 0003). credit_savings | buy_shares | external.
  payout_method           interest_payout_method NOT NULL DEFAULT 'credit_savings',
  payout_target_account_id uuid,                            -- credit_savings → deposit account
  payout_external_channel text,
  payout_external_ref     text,

  -- Posting outputs
  posted_at               timestamptz,
  posted_deposit_txn_id   uuid,                             -- deposit_transactions row, if credit_savings
  posted_share_txn_id     uuid,                             -- share_transactions row, if buy_shares

  notes                   text,
  UNIQUE (run_id, share_account_id)
);
CREATE INDEX dividend_run_lines_run_idx ON dividend_run_lines (run_id);
CREATE INDEX dividend_run_lines_member_idx ON dividend_run_lines (member_id, run_id);

-- ─────────── RLS ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['dividend_runs', 'dividend_run_lines'])
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

GRANT SELECT, INSERT, UPDATE, DELETE ON
  dividend_runs, dividend_run_lines
TO nexus_app;
