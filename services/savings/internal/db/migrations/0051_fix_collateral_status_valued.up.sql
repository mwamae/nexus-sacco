-- Bug fix — migration 0048 widened loan_collateral.status to include
-- 'offered', 'verified', 'pledged', 'released', 'auctioned' but left
-- 'valued' out. The handler + store layer flips
-- verified → valued on a successful valuation, but the CHECK rejected
-- it silently in cases where the UPDATE branched into the new value.
-- Widening the constraint here + repairing any row stuck in the wrong
-- state.

BEGIN;

ALTER TABLE loan_collateral
  DROP CONSTRAINT IF EXISTS loan_collateral_status_check;
ALTER TABLE loan_collateral
  ADD CONSTRAINT loan_collateral_status_check
  CHECK (status IN ('offered','verified','valued','pledged','released','auctioned'));

-- Repair any 'verified' row that already has a current valuation —
-- VerifyTx + CreateValuationTx are both safe to re-run, but this
-- back-fills the rows that hit the constraint pre-fix.
UPDATE loan_collateral
   SET status = 'valued'
 WHERE status = 'verified'
   AND EXISTS (
     SELECT 1 FROM collateral_valuations v
      WHERE v.collateral_id = loan_collateral.id AND v.is_current = true
   );

COMMIT;
