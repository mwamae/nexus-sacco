-- Down — drop in reverse dependency order. The loan_collateral
-- status column reverts to 'pledged' default; rows in the new
-- 'offered'/'verified' states are normalised to 'pledged' so the
-- pre-migration constraint succeeds when re-applied.

BEGIN;

DROP TABLE IF EXISTS loan_coverage_overrides;
DROP TABLE IF EXISTS loan_collateral_events;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS collateral_revaluation_months,
  DROP COLUMN IF EXISTS default_min_collateral_cover_pct,
  DROP COLUMN IF EXISTS default_min_guarantor_cover_pct,
  DROP COLUMN IF EXISTS default_security_model;

DROP TABLE IF EXISTS collateral_valuations;

ALTER TABLE loan_collateral
  DROP COLUMN IF EXISTS rejected_reason,
  DROP COLUMN IF EXISTS released_reason,
  DROP COLUMN IF EXISTS released_by,
  DROP COLUMN IF EXISTS released_at,
  DROP COLUMN IF EXISTS pledged_by,
  DROP COLUMN IF EXISTS pledged_at,
  DROP COLUMN IF EXISTS verification_photos,
  DROP COLUMN IF EXISTS verification_notes,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS verified_by,
  DROP COLUMN IF EXISTS proposed_at,
  DROP COLUMN IF EXISTS proposed_by;

UPDATE loan_collateral SET status = 'pledged'
 WHERE status NOT IN ('pledged','released','auctioned');

ALTER TABLE loan_collateral
  DROP CONSTRAINT IF EXISTS loan_collateral_status_check;
ALTER TABLE loan_collateral
  ADD CONSTRAINT loan_collateral_status_check
  CHECK (status IN ('pledged','released','auctioned'));
ALTER TABLE loan_collateral ALTER COLUMN status SET DEFAULT 'pledged';

ALTER TABLE loan_products
  DROP COLUMN IF EXISTS accepted_collateral_kinds,
  DROP COLUMN IF EXISTS min_collateral_cover_pct,
  DROP COLUMN IF EXISTS min_guarantor_cover_pct,
  DROP COLUMN IF EXISTS security_model;

COMMIT;
