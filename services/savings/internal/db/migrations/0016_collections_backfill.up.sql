-- Backfill the collections queue for loans already classified as
-- non-performing but never enqueued.
--
-- Why this exists: before today, the bridge from arrears_classification
-- → loan_collection_cases only fired from the nightly DPD cron. Three
-- other call sites (repayment posting, repayment reversal, manual
-- /recalc-dpd) updated the classification without ever opening a case;
-- and the provisioning test scaffold writes loans directly with the
-- classification + dpd already set, bypassing the DPD pipeline entirely.
-- See loan_store.go.SetCollections + loan_repayment_store.go.RecalcDPDTx
-- for the in-flight fix that closes the leak going forward.
--
-- This migration catches the historical drift. It opens an 'open' case
-- for every loan in arrears that doesn't already have one — using the
-- same INSERT shape as EnsureCaseForLoanTx so the rows are
-- indistinguishable from cases the Go code creates. Idempotent: the
-- WHERE NOT EXISTS guard means re-running this migration (or running
-- it on a database that's partially backfilled) does nothing for loans
-- already enqueued.
--
-- Priority follows the same scaling rule as EnsureCaseForLoanTx
-- (LEAST(days_past_due, 100)).
INSERT INTO loan_collection_cases (
  tenant_id, loan_id, member_id, status, classification_at_open, priority
)
SELECT
  l.tenant_id,
  l.id,
  l.member_id,
  'open',
  l.arrears_classification,
  LEAST(l.days_past_due, 100)
FROM loans l
WHERE l.arrears_classification IN ('watch', 'substandard', 'doubtful', 'loss')
  AND l.days_past_due > 0
  AND NOT EXISTS (
    SELECT 1 FROM loan_collection_cases c WHERE c.loan_id = l.id
  );
