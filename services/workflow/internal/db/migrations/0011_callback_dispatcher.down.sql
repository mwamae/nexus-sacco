DROP INDEX IF EXISTS wf_instances_callback_ready_idx;
ALTER TABLE wf_instances
  DROP COLUMN IF EXISTS callback_attempts,
  DROP COLUMN IF EXISTS callback_next_attempt_at,
  DROP COLUMN IF EXISTS callback_last_error;
