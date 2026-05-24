-- PR 1 of the BOSA / FOSA segmentation work — step 2 of 2.
--
-- 0027 added the `member_deposit` enum value; this migration:
--   1. introduces the segment enum (bosa | fosa)
--   2. tags every existing product as FOSA (member_deposit → BOSA)
--   3. adds the required-monthly-contribution columns
--   4. indexes (tenant_id, segment, is_active) for the segmented lists
--   5. seeds a default "Member Deposits" (BOSA) product per tenant so
--      the rest of the BOSA work has somewhere to attach
--
-- Idempotent: re-runs skip the seed for tenants that already have a
-- member_deposit product, and the IF NOT EXISTS guards cover the rest.

CREATE TYPE deposit_segment AS ENUM ('bosa', 'fosa');

ALTER TABLE deposit_products
  ADD COLUMN segment deposit_segment;

UPDATE deposit_products
  SET segment = CASE
    WHEN product_type::text = 'member_deposit' THEN 'bosa'::deposit_segment
    ELSE 'fosa'::deposit_segment
  END;

ALTER TABLE deposit_products ALTER COLUMN segment SET NOT NULL;

-- Required-contribution schedule. FOSA products leave both at the
-- defaults (0 / NULL). Day-of-month is capped at 28 so every month
-- has the same anchor — no special-casing Feb / 30-vs-31.
ALTER TABLE deposit_products
  ADD COLUMN required_monthly_amount  numeric(18,2) NOT NULL DEFAULT 0,
  ADD COLUMN required_day_of_month    int CHECK (required_day_of_month BETWEEN 1 AND 28);

CREATE INDEX deposit_products_segment_idx
  ON deposit_products (tenant_id, segment, is_active);

-- Per-tenant seed. The product is the BOSA bond — non-withdrawable,
-- redeemable on member exit, drives the new loan-multiplier basis.
-- Defaults (KES 1,000 opening, KES 500 monthly, day 5) match the
-- common SACCO baseline; tenants edit in /deposit-products.
INSERT INTO deposit_products (
  tenant_id, code, name, product_type, segment, description, is_active,
  min_opening_balance, min_operating_balance,
  notice_period_days, partial_withdrawal_allowed,
  eligibility, maturity_action, maintenance_fee_frequency,
  required_monthly_amount, required_day_of_month
)
SELECT
  t.id, 'MD', 'Member Deposits', 'member_deposit'::deposit_product_type, 'bosa'::deposit_segment,
  'Non-withdrawable member deposit bond — secures loans, redeemable on exit',
  true,
  1000, 0,
  0, false,
  'individuals'::deposit_eligibility, 'none'::deposit_maturity_action, 'none'::deposit_fee_frequency,
  500, 5
FROM tenants t
WHERE NOT EXISTS (
  SELECT 1 FROM deposit_products dp
   WHERE dp.tenant_id = t.id AND dp.product_type::text = 'member_deposit'
);
