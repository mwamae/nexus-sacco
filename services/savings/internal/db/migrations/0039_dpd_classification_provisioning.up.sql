-- Loans Phase 3 — DPD engine + classification history + IFRS 9 + provisioning v2.
--
-- Three new tables, three new tenant_operations columns, and extensions
-- to the existing provision_runs / provision_run_lines tables (NOT new
-- tables — those already exist in the codebase with 5 in-flight rows).
--
-- The Phase 3 spec called for `provisioning_runs` + `provisioning_run_lines`
-- as new tables, but the existing `provision_runs` + `provision_run_lines`
-- cover most of the columns. We ADD the Phase 3 fields on the existing
-- tables instead of duplicating the schema.

BEGIN;

-- ─────────── 1. loan_dpd_snapshots ───────────
--
-- Per-loan daily snapshot of DPD + classification. Source of truth
-- for every Phase 3 read path. One row per (loan, snapshot_date).
-- The daily dpd-classifier worker writes this; Phase 2 reports read
-- it (Reports.tsx PAR + SASRA + aging upgrade automatically once
-- snapshots exist for the period being queried).

CREATE TABLE IF NOT EXISTS loan_dpd_snapshots (
  id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id                    uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  snapshot_date              date NOT NULL,
  dpd_days                   int  NOT NULL,
  principal_balance          numeric(18,2) NOT NULL,
  interest_balance           numeric(18,2) NOT NULL,
  fees_balance               numeric(18,2) NOT NULL,
  penalty_balance            numeric(18,2) NOT NULL,
  classification_sasra       text NOT NULL,   -- performing | watch | substandard | doubtful | loss
  classification_ifrs9_stage int  NOT NULL,    -- 1 | 2 | 3
  next_due_date              date,
  computed_at                timestamptz NOT NULL DEFAULT now(),
  UNIQUE (loan_id, snapshot_date),
  CHECK (classification_sasra IN ('performing','watch','substandard','doubtful','loss')),
  CHECK (classification_ifrs9_stage IN (1, 2, 3))
);

CREATE INDEX IF NOT EXISTS loan_dpd_snapshots_tenant_date_idx
  ON loan_dpd_snapshots (tenant_id, snapshot_date DESC);
CREATE INDEX IF NOT EXISTS loan_dpd_snapshots_loan_date_idx
  ON loan_dpd_snapshots (loan_id, snapshot_date DESC);

ALTER TABLE loan_dpd_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_dpd_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_dpd_snapshots ON loan_dpd_snapshots
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_dpd_snapshots TO nexus_app;


-- ─────────── 2. loan_classification_history ───────────
--
-- Append-only log of classification changes. One row per change (not
-- per daily run — quiet days produce no rows). Powers the loan detail
-- "Classification timeline" tab + the audit trail.

CREATE TABLE IF NOT EXISTS loan_classification_history (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id              uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  changed_at           timestamptz NOT NULL DEFAULT now(),
  prev_sasra           text,
  new_sasra            text NOT NULL,
  prev_ifrs9_stage     int,
  new_ifrs9_stage      int  NOT NULL,
  dpd_days             int  NOT NULL,
  trigger_source       text NOT NULL,   -- daily_dpd_run | manual_override | repayment
  CHECK (new_sasra IN ('performing','watch','substandard','doubtful','loss')),
  CHECK (new_ifrs9_stage IN (1,2,3)),
  CHECK (trigger_source IN ('daily_dpd_run','manual_override','repayment'))
);

CREATE INDEX IF NOT EXISTS loan_classification_history_loan_idx
  ON loan_classification_history (loan_id, changed_at DESC);
CREATE INDEX IF NOT EXISTS loan_classification_history_tenant_idx
  ON loan_classification_history (tenant_id, changed_at DESC);

ALTER TABLE loan_classification_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_classification_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_classification_history ON loan_classification_history
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_classification_history TO nexus_app;


-- ─────────── 3. ecl_rate_matrix ───────────
--
-- Per-tenant classification → ECL% mapping. History-preserving via
-- effective_from in the PK so an auditor-approved rate change keeps
-- the prior row intact. Provisioning runs read the latest row with
-- effective_from <= period_end.

CREATE TABLE IF NOT EXISTS ecl_rate_matrix (
  tenant_id                  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  classification_sasra       text NOT NULL,
  classification_ifrs9_stage int  NOT NULL,
  ecl_rate_pct               numeric(7,4) NOT NULL,
  effective_from             date NOT NULL DEFAULT CURRENT_DATE,
  notes                      text,
  PRIMARY KEY (tenant_id, classification_sasra, classification_ifrs9_stage, effective_from),
  CHECK (classification_sasra IN ('performing','watch','substandard','doubtful','loss')),
  CHECK (classification_ifrs9_stage IN (1,2,3)),
  CHECK (ecl_rate_pct >= 0 AND ecl_rate_pct <= 1.0)
);

ALTER TABLE ecl_rate_matrix ENABLE ROW LEVEL SECURITY;
ALTER TABLE ecl_rate_matrix FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_ecl_rate_matrix ON ecl_rate_matrix
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON ecl_rate_matrix TO nexus_app;

-- Seed the standard CBK Prudential 0419 rates per tenant. Tenants
-- can edit via /settings/loans-policy after migration.
INSERT INTO ecl_rate_matrix (tenant_id, classification_sasra, classification_ifrs9_stage, ecl_rate_pct, notes)
SELECT t.id, x.cls, x.stage, x.rate, x.note
  FROM tenants t
  CROSS JOIN (VALUES
    ('performing',   1, 0.0100, 'CBK Prudential 0419 — Normal (1% general provision)'),
    ('watch',        1, 0.0500, 'Watch — 5%'),
    ('substandard',  2, 0.2500, 'Substandard — 25%'),
    ('doubtful',     3, 0.5000, 'Doubtful — 50%'),
    ('loss',         3, 1.0000, 'Loss — 100%')
  ) AS x(cls, stage, rate, note)
ON CONFLICT DO NOTHING;


-- ─────────── 4. tenant_operations — three NEW DPD-threshold columns ───────────
--
-- The existing dpd_substandard_days / dpd_doubtful_days / dpd_loss_days
-- columns already cover three of the SASRA thresholds. We add the
-- watch threshold (which the existing schema implicitly assumed at
-- DPD=1) and the two IFRS 9 stage thresholds.

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS sasra_watch_dpd     int NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS ifrs9_stage2_dpd    int NOT NULL DEFAULT 31,
  ADD COLUMN IF NOT EXISTS ifrs9_stage3_dpd    int NOT NULL DEFAULT 91;

COMMENT ON COLUMN tenant_operations.sasra_watch_dpd IS
  'DPD threshold for SASRA "watch" classification. Default 1 day past due.';
COMMENT ON COLUMN tenant_operations.dpd_substandard_days IS
  'DPD threshold for SASRA "substandard" classification. Pre-existed Phase 3; reused as-is.';
COMMENT ON COLUMN tenant_operations.ifrs9_stage2_dpd IS
  'DPD threshold for IFRS 9 stage 2 (significant increase in credit risk). Default 31 days per CBK guidance.';
COMMENT ON COLUMN tenant_operations.ifrs9_stage3_dpd IS
  'DPD threshold for IFRS 9 stage 3 (credit-impaired). Default 91 days per CBK guidance.';


-- ─────────── 5. provision_runs — Phase 3 extensions ───────────
--
-- The existing provision_runs has as_of_date (any date) + status in
-- (pending/computed/posted/superseded). Phase 3 adds month-grouping
-- semantics + draft/cancel flow + IFRS 9 fields.

ALTER TABLE provision_runs
  ADD COLUMN IF NOT EXISTS period_month   date,
  ADD COLUMN IF NOT EXISTS cancelled_at   timestamptz,
  ADD COLUMN IF NOT EXISTS cancelled_by   uuid,
  ADD COLUMN IF NOT EXISTS cancel_reason  text,
  -- journal_entry_id (uuid) parallel to existing journal_entry_ref (text)
  -- so Phase 3 can store the JE id from the new accounting service path
  -- without breaking the old field; Phase 4 can rationalise.
  ADD COLUMN IF NOT EXISTS journal_entry_id uuid;

-- Extend the status CHECK to admit the Phase 3 flow states. The old
-- statuses (pending/computed/posted/failed/superseded) are kept so the
-- 5 in-flight rows remain valid; Phase 3 adds 'draft' (computed but
-- not yet posted) and 'cancelled' (operator aborted before posting).
ALTER TABLE provision_runs DROP CONSTRAINT IF EXISTS provision_runs_status_check;
ALTER TABLE provision_runs ADD  CONSTRAINT provision_runs_status_check
  CHECK (status IN ('pending','draft','computed','posted','failed','superseded','cancelled'));

-- Phase 3 unique constraint on (tenant_id, period_month) when set —
-- partial index so old as_of_date-only rows don't trip the constraint.
CREATE UNIQUE INDEX IF NOT EXISTS provision_runs_tenant_month_unique
  ON provision_runs (tenant_id, period_month)
  WHERE period_month IS NOT NULL AND status IN ('draft','computed','posted');


-- ─────────── 6. provision_run_lines — Phase 3 extensions ───────────

ALTER TABLE provision_run_lines
  ADD COLUMN IF NOT EXISTS product_id                 uuid REFERENCES loan_products(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS classification_ifrs9_stage int,
  -- delta is the signed amount (provision_amount − previous_provision).
  -- Sum across all lines on a run = run.movement (already exists).
  ADD COLUMN IF NOT EXISTS delta                      numeric(18,2);

-- Backfill product_id from loans for any in-flight lines.
UPDATE provision_run_lines prl
   SET product_id = l.product_id
  FROM loans l
 WHERE prl.loan_id = l.id
   AND prl.product_id IS NULL;

COMMIT;
