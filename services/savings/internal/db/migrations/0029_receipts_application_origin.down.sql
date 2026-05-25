-- Roll back the application-origin receipt stamping. Removes the
-- synthetic rows the up migration backfilled (and any added since
-- by the stamper at materialise time), then drops the new columns
-- and indexes, and finally restores the original two-branch
-- receipts_check.

DELETE FROM receipt_lines
 WHERE receipt_id IN (
   SELECT id FROM receipts WHERE application_payment_id IS NOT NULL
 );
DELETE FROM receipts WHERE application_payment_id IS NOT NULL;

DROP INDEX IF EXISTS receipts_application_payment_uidx;
DROP INDEX IF EXISTS receipts_application_id_idx;

ALTER TABLE receipts DROP CONSTRAINT IF EXISTS receipts_check;
ALTER TABLE receipts ADD CONSTRAINT receipts_check CHECK (
  (channel = 'cash'::receipt_channel
    AND till_session_id IS NOT NULL
    AND virtual_till_id IS NULL
    AND channel_ref IS NULL)
  OR
  (channel <> 'cash'::receipt_channel
    AND virtual_till_id IS NOT NULL
    AND till_session_id IS NULL
    AND channel_ref IS NOT NULL)
);

ALTER TABLE receipts
  DROP COLUMN IF EXISTS posted_outside_till,
  DROP COLUMN IF EXISTS application_payment_id,
  DROP COLUMN IF EXISTS application_id;
