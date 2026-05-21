-- ═══════════════════════════════════════════════════════════════════
-- Loan Loss Provisioning (Phase 11/3) — SASRA-aligned classification
-- and IFRS-style provisioning ledger.
--
-- A provision run snapshots the portfolio on a given as-of date:
--   * classifies every active loan into performing/watch/substandard/
--     doubtful/loss based on days past due
--   * computes the required provision per loan using the SASRA matrix
--   * stores a per-loan line for audit
--   * posts the *movement* (this run total − previous run total) to
--     the GL as a single journal entry:
--       DR 5210 Loan Loss Provisioning Expense
--       CR 1120 Loan Loss Provision  (contra-asset)
--     (or the reverse if the portfolio improved and provisions
--      are being released).
--
-- Re-running a run for the same as-of date is allowed — superseded
-- runs are kept for audit but their GL impact must be reversed
-- before posting the new one. We keep that policy in the handler;
-- DB-level we only require that at most one *posted* run per
-- tenant+as_of_date exists at a time.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS provision_runs (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  as_of_date          date NOT NULL,
  status              text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'computed', 'posted', 'failed', 'superseded')),
  loans_classified    int  NOT NULL DEFAULT 0,
  total_outstanding   numeric(18,2) NOT NULL DEFAULT 0,
  total_provision     numeric(18,2) NOT NULL DEFAULT 0,
  previous_provision  numeric(18,2) NOT NULL DEFAULT 0,
  movement            numeric(18,2) NOT NULL DEFAULT 0,
  journal_entry_ref   text,
  notes               text,
  computed_at         timestamptz,
  posted_at           timestamptz,
  posted_by           uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS provision_runs_tenant_idx
  ON provision_runs (tenant_id, as_of_date DESC);
CREATE UNIQUE INDEX IF NOT EXISTS provision_runs_posted_unique
  ON provision_runs (tenant_id, as_of_date) WHERE status = 'posted';

ALTER TABLE provision_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE provision_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_provision_runs ON provision_runs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON provision_runs TO nexus_app;


CREATE TABLE IF NOT EXISTS provision_run_lines (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id              uuid NOT NULL REFERENCES provision_runs(id) ON DELETE CASCADE,
  loan_id             uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  member_id           uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  loan_no             text NOT NULL,
  days_past_due       int  NOT NULL,
  classification      text NOT NULL
                       CHECK (classification IN ('performing','watch','substandard','doubtful','loss')),
  outstanding         numeric(18,2) NOT NULL,
  provision_rate      numeric(6,4)  NOT NULL,            -- 0.0100, 0.0500, 0.2500, 0.5000, 1.0000
  provision_amount    numeric(18,2) NOT NULL,
  previous_classification text,
  previous_provision  numeric(18,2) NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS provision_run_lines_run_idx
  ON provision_run_lines (run_id, classification, outstanding DESC);
CREATE INDEX IF NOT EXISTS provision_run_lines_loan_idx
  ON provision_run_lines (loan_id);

ALTER TABLE provision_run_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE provision_run_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_provision_run_lines ON provision_run_lines
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON provision_run_lines TO nexus_app;
