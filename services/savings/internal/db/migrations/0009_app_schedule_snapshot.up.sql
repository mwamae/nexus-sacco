-- Amortization schedule snapshot on the loan_applications row.
--
-- Computed at submission time using requested amount / term + product
-- config (rate, methods, grace). Re-computed when an application is
-- approved with different amount or term. Read-only thereafter — when
-- the loan is disbursed, the canonical schedule lives in
-- loan_repayment_schedule rows.

ALTER TABLE loan_applications
  ADD COLUMN IF NOT EXISTS repayment_schedule_snapshot       jsonb,
  ADD COLUMN IF NOT EXISTS repayment_schedule_snapshot_at    timestamptz,
  ADD COLUMN IF NOT EXISTS repayment_schedule_total_payable  numeric(18,2),
  ADD COLUMN IF NOT EXISTS repayment_schedule_total_interest numeric(18,2),
  ADD COLUMN IF NOT EXISTS repayment_schedule_installment    numeric(18,2);

COMMENT ON COLUMN loan_applications.repayment_schedule_snapshot IS
  'Projected installment plan generated at application time. JSON array of {installment_no, due_date, principal_due, interest_due, total_due, outstanding_after}.';
