-- Rolls back 0030. Drops the three new toggles + the audit table.
-- Does NOT restore the per-tenant rows the backfill flipped — once
-- a tenant has been pushed to safe defaults, leaving them there on
-- rollback is the right call. Restoring defaults to FALSE on the
-- 15 existing columns DOES revert (only affects future inserts).

DROP TRIGGER IF EXISTS tenant_approval_changes_no_update ON tenant_approval_changes;
DROP TRIGGER IF EXISTS tenant_approval_changes_no_delete ON tenant_approval_changes;
DROP FUNCTION IF EXISTS tenant_approval_changes_no_mutate();
DROP TABLE IF EXISTS tenant_approval_changes;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS approval_application_fee,
  DROP COLUMN IF EXISTS approval_welfare_collection,
  DROP COLUMN IF EXISTS approval_fee_collection;

ALTER TABLE tenant_operations
  ALTER COLUMN approval_deposit                  SET DEFAULT false,
  ALTER COLUMN approval_withdrawal               SET DEFAULT false,
  ALTER COLUMN approval_deposit_transfer         SET DEFAULT false,
  ALTER COLUMN approval_share_purchase           SET DEFAULT false,
  ALTER COLUMN approval_share_transfer           SET DEFAULT false,
  ALTER COLUMN approval_share_bonus              SET DEFAULT false,
  ALTER COLUMN approval_share_lien               SET DEFAULT false,
  ALTER COLUMN approval_loan_disbursement        SET DEFAULT false,
  ALTER COLUMN approval_loan_repayment           SET DEFAULT false,
  ALTER COLUMN approval_loan_settle              SET DEFAULT false,
  ALTER COLUMN approval_loan_reverse             SET DEFAULT false,
  ALTER COLUMN approval_loan_writeoff            SET DEFAULT false,
  ALTER COLUMN approval_loan_reschedule          SET DEFAULT false,
  ALTER COLUMN approval_loan_moratorium          SET DEFAULT false,
  ALTER COLUMN approval_loan_settlement_discount SET DEFAULT false;
