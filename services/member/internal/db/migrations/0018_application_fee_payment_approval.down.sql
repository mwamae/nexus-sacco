DROP INDEX IF EXISTS application_fee_payments_approval_idx;
ALTER TABLE application_fee_payments DROP COLUMN IF EXISTS approval_id;
