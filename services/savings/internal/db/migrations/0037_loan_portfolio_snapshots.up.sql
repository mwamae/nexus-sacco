-- Loans Phase 2 — daily portfolio snapshot table.
--
-- The Reports page renders trend charts (PAR-30 over time, portfolio
-- growth over time) that need historical points. Rather than walk
-- loan_transactions for every chart render, we materialise one row
-- per tenant per day from a small worker (cmd/loan-snapshotter).
-- Charts then become trivial range scans on this table.
--
-- The snapshotter is idempotent on (tenant_id, snapshot_date) — re-
-- running for the same day replaces the row, so an operator can
-- rerun after a midnight outage without producing duplicates.
--
-- On first run for a tenant the worker backfills the last 90 days
-- by reconstructing each day's principal_balance + dpd from
-- loan_transactions. See cmd/loan-snapshotter/main.go for the
-- reconstruction logic. Auditor sign-off is NOT required for the
-- backfill — these are aggregate metrics, not ledger entries.
--
-- DPD-based fields (par*_principal, in_arrears_count) use the
-- Phase 1 DPD proxy (CURRENT_DATE - next_installment_due_at).
-- Phase 3's real classification engine swaps in loan_dpd_snapshots.dpd_days;
-- the schema here doesn't change — only the snapshotter's compute
-- path does.

CREATE TABLE IF NOT EXISTS loan_portfolio_snapshots (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  snapshot_date       date NOT NULL,

  -- Outstanding balance buckets across active+in_arrears+restructured.
  total_principal     numeric(18,2) NOT NULL DEFAULT 0,
  total_interest      numeric(18,2) NOT NULL DEFAULT 0,
  total_fees          numeric(18,2) NOT NULL DEFAULT 0,
  total_penalty       numeric(18,2) NOT NULL DEFAULT 0,

  -- PAR principal (denominator: total_principal).
  par1_principal      numeric(18,2) NOT NULL DEFAULT 0,
  par30_principal     numeric(18,2) NOT NULL DEFAULT 0,
  par90_principal     numeric(18,2) NOT NULL DEFAULT 0,

  -- Loan counts per status. Useful for the "loan count over time"
  -- secondary chart on the Reports → PAR tab.
  active_count        int NOT NULL DEFAULT 0,
  in_arrears_count    int NOT NULL DEFAULT 0,
  restructured_count  int NOT NULL DEFAULT 0,

  -- Per-product breakdown for the donut-trend chart. JSON shape:
  --   [{"product_id":"…","outstanding":"…","active_count":N}, …]
  -- Stored as jsonb so the snapshotter can write it in one INSERT
  -- without a child table.
  by_product          jsonb,

  created_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, snapshot_date)
);

CREATE INDEX IF NOT EXISTS loan_portfolio_snapshots_tenant_date_idx
  ON loan_portfolio_snapshots (tenant_id, snapshot_date DESC);

ALTER TABLE loan_portfolio_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_portfolio_snapshots FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_loan_portfolio_snapshots ON loan_portfolio_snapshots
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON loan_portfolio_snapshots TO nexus_app;
