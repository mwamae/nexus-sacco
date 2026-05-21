ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS approval_loan_settlement_discount,
  DROP COLUMN IF EXISTS approval_loan_moratorium,
  DROP COLUMN IF EXISTS approval_loan_reschedule,
  DROP COLUMN IF EXISTS approval_loan_writeoff,
  DROP COLUMN IF EXISTS approval_loan_reverse,
  DROP COLUMN IF EXISTS approval_loan_settle,
  DROP COLUMN IF EXISTS approval_loan_repayment,
  DROP COLUMN IF EXISTS approval_loan_disbursement;
