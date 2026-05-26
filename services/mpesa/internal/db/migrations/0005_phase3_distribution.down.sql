DROP INDEX IF EXISTS receipt_lines_external_validation_ref_idx;
ALTER TABLE receipt_lines DROP COLUMN IF EXISTS external_validation_ref;

ALTER TABLE mpesa_distribution_runs
  DROP COLUMN IF EXISTS amount,
  DROP COLUMN IF EXISTS resolved_via,
  DROP COLUMN IF EXISTS resolved_member_id,
  DROP COLUMN IF EXISTS clearing_account_code,
  DROP COLUMN IF EXISTS cash_account_code;

DROP INDEX IF EXISTS mpesa_inbound_events_distributor_idx;
ALTER TABLE mpesa_inbound_events
  DROP COLUMN IF EXISTS locked_by,
  DROP COLUMN IF EXISTS locked_at,
  DROP COLUMN IF EXISTS distribution_run_id,
  DROP COLUMN IF EXISTS posted_at,
  DROP COLUMN IF EXISTS error_text,
  DROP COLUMN IF EXISTS attempts;
