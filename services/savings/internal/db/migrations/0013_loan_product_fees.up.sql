-- ═══════════════════════════════════════════════════════════════════
-- Flexible fee list per loan product (Phase 7d).
--
-- Replaces the 3 hard-coded fee column-triples on loan_products
-- (processing / insurance / appraisal) with a sub-table that lets a
-- SACCO define any number of fees per product, give each a custom
-- label, and remove any of them.
--
-- Backfills existing fees (only those with amount > 0) so deployments
-- with data don't lose their config, then drops the old columns.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS loan_product_fees (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  product_id    uuid NOT NULL REFERENCES loan_products(id) ON DELETE CASCADE,
  name          text NOT NULL,
  amount        numeric(18,2) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  is_pct        boolean NOT NULL DEFAULT false,
  timing        loan_fee_timing NOT NULL DEFAULT 'upfront',
  display_order int NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS loan_product_fees_product_idx
  ON loan_product_fees (product_id, display_order);

ALTER TABLE loan_product_fees ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_product_fees FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_product_fees ON loan_product_fees
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON loan_product_fees TO nexus_app;

-- Backfill from the existing 3-column layout. Only emit rows for fees
-- that were actually configured (amount > 0) so we don't pollute
-- products that never set a particular fee.
INSERT INTO loan_product_fees (tenant_id, product_id, name, amount, is_pct, timing, display_order)
SELECT tenant_id, id, 'Processing fee',
       processing_fee, processing_fee_is_pct, processing_fee_timing, 1
FROM loan_products WHERE processing_fee > 0;

INSERT INTO loan_product_fees (tenant_id, product_id, name, amount, is_pct, timing, display_order)
SELECT tenant_id, id, 'Insurance / LPF fee',
       insurance_fee, insurance_fee_is_pct, insurance_fee_timing, 2
FROM loan_products WHERE insurance_fee > 0;

INSERT INTO loan_product_fees (tenant_id, product_id, name, amount, is_pct, timing, display_order)
SELECT tenant_id, id, 'Appraisal fee',
       appraisal_fee, appraisal_fee_is_pct, appraisal_fee_timing, 3
FROM loan_products WHERE appraisal_fee > 0;

-- Drop the old hard-coded columns.
ALTER TABLE loan_products
  DROP COLUMN IF EXISTS processing_fee,
  DROP COLUMN IF EXISTS processing_fee_is_pct,
  DROP COLUMN IF EXISTS processing_fee_timing,
  DROP COLUMN IF EXISTS insurance_fee,
  DROP COLUMN IF EXISTS insurance_fee_is_pct,
  DROP COLUMN IF EXISTS insurance_fee_timing,
  DROP COLUMN IF EXISTS appraisal_fee,
  DROP COLUMN IF EXISTS appraisal_fee_is_pct,
  DROP COLUMN IF EXISTS appraisal_fee_timing;
