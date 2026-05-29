-- Phase 1.5a — Collateral foundation.
--
-- Adds the per-product security_model + min cover %, expands the
-- loan_collateral lifecycle from {pledged,released,auctioned} to the
-- full {offered,verified,pledged,released,auctioned} chain with
-- timestamps + actor columns for each transition, creates a
-- supersede-friendly collateral_valuations history table, an
-- append-only loan_collateral_events audit log, an override-audit
-- row for senior staff bypassing the approval coverage gate, and
-- per-tenant defaults on tenant_operations.
--
-- See nexusSacco_Loans_Phase_1_5a_Collateral_Foundation_Prompt.md for
-- the full spec; the lifecycle matrix is enforced server-side.

BEGIN;

-- ─────────── 1. Per-product security policy ───────────

ALTER TABLE loan_products
  ADD COLUMN IF NOT EXISTS security_model text NOT NULL DEFAULT 'guarantor_only'
    CHECK (security_model IN ('none','guarantor_only','collateral_only','either','both')),
  ADD COLUMN IF NOT EXISTS min_guarantor_cover_pct numeric(5,2) NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS min_collateral_cover_pct numeric(5,2) NOT NULL DEFAULT 125,
  -- NULL = all kinds accepted. Non-NULL narrows the accepted set (a
  -- property loan can accept only 'title_deed', etc.). Validated in
  -- the handler against the loan_collateral_kind enum.
  ADD COLUMN IF NOT EXISTS accepted_collateral_kinds text[];

COMMENT ON COLUMN loan_products.security_model IS
  'Which external security is required: none, guarantor_only, collateral_only, either (one or the other), both (both required).';
COMMENT ON COLUMN loan_products.min_guarantor_cover_pct IS
  'Sum of accepted guarantor pledges must be at least this percent of the loan amount.';
COMMENT ON COLUMN loan_products.min_collateral_cover_pct IS
  'Sum of pledged collateral FSV must be at least this percent of the loan amount.';
COMMENT ON COLUMN loan_products.accepted_collateral_kinds IS
  'NULL = all kinds. Array of loan_collateral_kind values that count as security for this product.';

-- ─────────── 2. Expand loan_collateral lifecycle ───────────
--
-- Existing rows default to status='pledged'; we widen the constraint
-- to the new five-state set. Any legacy values outside the original
-- three are normalised to 'pledged' first so the new CHECK succeeds.
UPDATE loan_collateral
   SET status = 'pledged'
 WHERE status NOT IN ('pledged','released','auctioned');

ALTER TABLE loan_collateral
  DROP CONSTRAINT IF EXISTS loan_collateral_status_check;
ALTER TABLE loan_collateral
  ADD CONSTRAINT loan_collateral_status_check
  CHECK (status IN ('offered','verified','pledged','released','auctioned'));

-- New default for newly-created rows is 'offered' — they must walk
-- through the verify+value steps before they're considered pledged.
ALTER TABLE loan_collateral
  ALTER COLUMN status SET DEFAULT 'offered';

ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS proposed_by         uuid,
  ADD COLUMN IF NOT EXISTS proposed_at         timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS verified_by         uuid,
  ADD COLUMN IF NOT EXISTS verified_at         timestamptz,
  ADD COLUMN IF NOT EXISTS verification_notes  text,
  ADD COLUMN IF NOT EXISTS verification_photos jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS pledged_at          timestamptz,
  ADD COLUMN IF NOT EXISTS pledged_by          uuid,
  ADD COLUMN IF NOT EXISTS released_at         timestamptz,
  ADD COLUMN IF NOT EXISTS released_by         uuid,
  ADD COLUMN IF NOT EXISTS released_reason     text,
  ADD COLUMN IF NOT EXISTS rejected_reason     text;

-- ─────────── 3. Valuation history (one current per item) ───────────

CREATE TABLE collateral_valuations (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id         uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  valuer_name           text NOT NULL,
  valuer_contact        text,
  valuation_date        date NOT NULL,
  market_value          numeric(18,2) NOT NULL CHECK (market_value > 0),
  forced_sale_value     numeric(18,2) NOT NULL CHECK (forced_sale_value > 0),
  valuation_report_path text,
  expires_at            date,
  is_current            boolean NOT NULL DEFAULT true,
  superseded_by_id      uuid REFERENCES collateral_valuations(id),
  notes                 text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  created_by            uuid NOT NULL
);

CREATE INDEX collateral_valuations_collateral_idx
  ON collateral_valuations (collateral_id, is_current);
CREATE INDEX collateral_valuations_expiring_idx
  ON collateral_valuations (tenant_id, expires_at)
  WHERE is_current = true AND expires_at IS NOT NULL;
-- At most one current valuation per collateral item.
CREATE UNIQUE INDEX collateral_valuations_one_current_idx
  ON collateral_valuations (collateral_id)
  WHERE is_current = true;

ALTER TABLE collateral_valuations ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_valuations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_valuations ON collateral_valuations
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON collateral_valuations TO nexus_app;

-- ─────────── 4. Per-tenant defaults + revaluation cadence ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS default_security_model text NOT NULL DEFAULT 'guarantor_only'
    CHECK (default_security_model IN ('none','guarantor_only','collateral_only','either','both')),
  ADD COLUMN IF NOT EXISTS default_min_guarantor_cover_pct numeric(5,2) NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS default_min_collateral_cover_pct numeric(5,2) NOT NULL DEFAULT 125,
  ADD COLUMN IF NOT EXISTS collateral_revaluation_months int NOT NULL DEFAULT 24
    CHECK (collateral_revaluation_months BETWEEN 1 AND 120);

-- ─────────── 5. Lifecycle audit log ───────────
--
-- Mirrors the loan_collection_events shape (services/savings/.../0040_collections.up.sql).
-- Append-only; rows are never updated or deleted.
CREATE TABLE loan_collateral_events (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id  uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  actor_user_id  uuid,
  kind           text NOT NULL CHECK (kind IN (
    'proposed','verified','valued','pledged','released','rejected','auctioned',
    'revalued','documents_attached','note_added','coverage_override'
  )),
  details        jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX loan_collateral_events_collateral_idx
  ON loan_collateral_events (collateral_id, occurred_at DESC);
CREATE INDEX loan_collateral_events_tenant_kind_idx
  ON loan_collateral_events (tenant_id, kind, occurred_at DESC);

ALTER TABLE loan_collateral_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_collateral_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_collateral_events ON loan_collateral_events
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON loan_collateral_events TO nexus_app;

-- ─────────── 6. Coverage-override audit row ───────────
--
-- One row per approver bypass of the security-coverage gate. Append-only.
-- The override carries the snapshot of the policy + coverage at the
-- time of the override, so a future auditor can reconstruct what the
-- approver saw without joining against later-mutated rows.
CREATE TABLE loan_coverage_overrides (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id        uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  overridden_by         uuid NOT NULL,
  occurred_at           timestamptz NOT NULL DEFAULT now(),
  reason                text NOT NULL,
  security_model        text NOT NULL,
  loan_amount           numeric(18,2) NOT NULL,
  guarantor_pledged     numeric(18,2) NOT NULL DEFAULT 0,
  collateral_fsv        numeric(18,2) NOT NULL DEFAULT 0,
  min_guarantor_cover_pct  numeric(5,2) NOT NULL,
  min_collateral_cover_pct numeric(5,2) NOT NULL,
  guarantor_cover_pct      numeric(7,2) NOT NULL,
  collateral_cover_pct     numeric(7,2) NOT NULL,
  evaluator_reason         text
);
CREATE INDEX loan_coverage_overrides_app_idx
  ON loan_coverage_overrides (application_id, occurred_at DESC);

ALTER TABLE loan_coverage_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_coverage_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_coverage_overrides ON loan_coverage_overrides
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON loan_coverage_overrides TO nexus_app;

COMMIT;
