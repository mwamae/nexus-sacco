DROP TABLE IF EXISTS member_fees_due;

DROP INDEX IF EXISTS deposit_transactions_external_ref_idx;
DROP INDEX IF EXISTS loan_transactions_external_ref_idx;

ALTER TABLE deposit_transactions DROP COLUMN IF EXISTS external_validation_ref;
ALTER TABLE loan_transactions    DROP COLUMN IF EXISTS external_validation_ref;
