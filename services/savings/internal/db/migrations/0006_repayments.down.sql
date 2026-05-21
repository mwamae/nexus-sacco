DROP INDEX IF EXISTS loan_schedule_accrual_idx;
ALTER TABLE loan_repayment_schedule
  DROP COLUMN IF EXISTS accrued_interest_txn_id,
  DROP COLUMN IF EXISTS accrued_at;
