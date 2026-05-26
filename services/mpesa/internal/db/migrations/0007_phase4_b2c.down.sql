-- mpesa_b2c_reversal definitions
DELETE FROM wf_levels      WHERE definition_id IN
  (SELECT id FROM wf_definitions WHERE process_kind = 'mpesa_b2c_reversal');
DELETE FROM wf_definitions WHERE process_kind = 'mpesa_b2c_reversal';

DELETE FROM chart_of_accounts WHERE code IN ('1015', '1099');

DROP INDEX IF EXISTS mpesa_outbound_requests_finalize_idx;
DROP INDEX IF EXISTS mpesa_outbound_requests_dispatch_idx;

ALTER TABLE mpesa_outbound_requests
  DROP COLUMN IF EXISTS locked_by,
  DROP COLUMN IF EXISTS locked_at,
  DROP COLUMN IF EXISTS finalization_error,
  DROP COLUMN IF EXISTS finalized_at,
  DROP COLUMN IF EXISTS finalization_attempts,
  DROP COLUMN IF EXISTS finalization_status,
  DROP COLUMN IF EXISTS mpesa_receipt_number,
  DROP COLUMN IF EXISTS result_raw;

DROP TYPE IF EXISTS mpesa_outbound_finalization_status;

DROP INDEX IF EXISTS mpesa_paybills_default_idx;
ALTER TABLE mpesa_paybills DROP COLUMN IF EXISTS is_default;

-- mpesa_outbound_status += 'reversed' is forward-only (postgres can't
-- safely drop an enum value).
