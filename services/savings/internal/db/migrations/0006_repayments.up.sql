-- ═══════════════════════════════════════════════════════════════════
-- Repayments + DPD additions.
--
-- Adds:
--   • loan_repayment_schedule.accrued_at — when this installment's
--     interest_due became "live" on the loan account. The daily DPD
--     job marks it; remains NULL until the due date arrives.
--   • loan_repayment_schedule.accrued_interest_txn_id — pointer to
--     the loan_transactions row that posted the interest accrual.
--
-- ═══════════════════════════════════════════════════════════════════

ALTER TABLE loan_repayment_schedule
  ADD COLUMN IF NOT EXISTS accrued_at                timestamptz,
  ADD COLUMN IF NOT EXISTS accrued_interest_txn_id   uuid REFERENCES loan_transactions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS loan_schedule_accrual_idx ON loan_repayment_schedule (tenant_id, due_date)
  WHERE accrued_at IS NULL;
