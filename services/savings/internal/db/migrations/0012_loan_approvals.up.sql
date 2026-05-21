-- Stage 3 of the maker-checker rollout — loan cash actions.
--
-- Adds per-kind approval toggles for:
--   • loan_disbursement       — net disbursement to the borrower
--   • loan_repayment          — borrower-side repayments via any channel
--   • loan_settle             — early full settlement
--   • loan_reverse            — reversing a posted repayment (or other txn)
--   • loan_writeoff           — board write-off
--   • loan_reschedule         — re-amortise over a new term
--   • loan_moratorium         — payment holiday
--   • loan_settlement_discount — accept less as full payment

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS approval_loan_disbursement       boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_repayment          boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_settle             boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_reverse            boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_writeoff           boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_reschedule         boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_moratorium         boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_loan_settlement_discount boolean NOT NULL DEFAULT false;
