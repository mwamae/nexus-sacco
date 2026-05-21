-- Re-add the 3 hard-coded fee columns and best-effort copy back from
-- loan_product_fees. Any extra/custom-named fees are lost on rollback.

ALTER TABLE loan_products
  ADD COLUMN processing_fee        numeric(18,2) NOT NULL DEFAULT 0,
  ADD COLUMN processing_fee_is_pct boolean       NOT NULL DEFAULT true,
  ADD COLUMN processing_fee_timing loan_fee_timing NOT NULL DEFAULT 'upfront',
  ADD COLUMN insurance_fee         numeric(18,2) NOT NULL DEFAULT 0,
  ADD COLUMN insurance_fee_is_pct  boolean       NOT NULL DEFAULT true,
  ADD COLUMN insurance_fee_timing  loan_fee_timing NOT NULL DEFAULT 'upfront',
  ADD COLUMN appraisal_fee         numeric(18,2) NOT NULL DEFAULT 0,
  ADD COLUMN appraisal_fee_is_pct  boolean       NOT NULL DEFAULT false,
  ADD COLUMN appraisal_fee_timing  loan_fee_timing NOT NULL DEFAULT 'upfront';

UPDATE loan_products lp SET
  processing_fee        = COALESCE(f.amount, 0),
  processing_fee_is_pct = COALESCE(f.is_pct, true),
  processing_fee_timing = COALESCE(f.timing, 'upfront')
FROM (SELECT product_id, amount, is_pct, timing FROM loan_product_fees WHERE name ILIKE '%processing%') f
WHERE f.product_id = lp.id;

UPDATE loan_products lp SET
  insurance_fee        = COALESCE(f.amount, 0),
  insurance_fee_is_pct = COALESCE(f.is_pct, true),
  insurance_fee_timing = COALESCE(f.timing, 'upfront')
FROM (SELECT product_id, amount, is_pct, timing FROM loan_product_fees WHERE name ILIKE '%insurance%' OR name ILIKE '%lpf%') f
WHERE f.product_id = lp.id;

UPDATE loan_products lp SET
  appraisal_fee        = COALESCE(f.amount, 0),
  appraisal_fee_is_pct = COALESCE(f.is_pct, false),
  appraisal_fee_timing = COALESCE(f.timing, 'upfront')
FROM (SELECT product_id, amount, is_pct, timing FROM loan_product_fees WHERE name ILIKE '%appraisal%') f
WHERE f.product_id = lp.id;

DROP TABLE IF EXISTS loan_product_fees;
