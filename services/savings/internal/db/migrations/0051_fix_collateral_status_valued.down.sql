-- Reverts to the 0048 (incorrect) constraint set. Note: this re-introduces
-- the 'valued' silent-reject bug, so down-migrating in prod is unsafe.

BEGIN;

UPDATE loan_collateral SET status = 'verified' WHERE status = 'valued';
ALTER TABLE loan_collateral DROP CONSTRAINT IF EXISTS loan_collateral_status_check;
ALTER TABLE loan_collateral
  ADD CONSTRAINT loan_collateral_status_check
  CHECK (status IN ('offered','verified','pledged','released','auctioned'));

COMMIT;
