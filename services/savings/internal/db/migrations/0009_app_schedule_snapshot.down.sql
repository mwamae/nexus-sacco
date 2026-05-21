ALTER TABLE loan_applications
  DROP COLUMN IF EXISTS repayment_schedule_installment,
  DROP COLUMN IF EXISTS repayment_schedule_total_interest,
  DROP COLUMN IF EXISTS repayment_schedule_total_payable,
  DROP COLUMN IF EXISTS repayment_schedule_snapshot_at,
  DROP COLUMN IF EXISTS repayment_schedule_snapshot;
