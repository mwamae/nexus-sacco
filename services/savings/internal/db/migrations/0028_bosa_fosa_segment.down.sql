-- Roll back the BOSA / FOSA additions. Drops the segment column,
-- the new required-contribution columns, the index, and the seeded
-- member_deposit products. The `member_deposit` enum value cannot
-- be removed in Postgres without dropping deposit_product_type
-- entirely, so it stays — harmless once no product references it.

-- Remove the seeded BOSA products first so the column drop doesn't
-- leak data into a re-up.
DELETE FROM deposit_products WHERE product_type::text = 'member_deposit';

DROP INDEX IF EXISTS deposit_products_segment_idx;

ALTER TABLE deposit_products
  DROP COLUMN IF EXISTS required_day_of_month,
  DROP COLUMN IF EXISTS required_monthly_amount,
  DROP COLUMN IF EXISTS segment;

DROP TYPE IF EXISTS deposit_segment;
