DROP INDEX IF EXISTS applications_materialized_cp_idx;
ALTER TABLE membership_applications DROP COLUMN IF EXISTS materialized_counterparty_id;
