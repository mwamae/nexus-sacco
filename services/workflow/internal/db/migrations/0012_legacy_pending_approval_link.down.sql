DROP INDEX IF EXISTS wf_instances_legacy_pa_idx;
ALTER TABLE wf_instances DROP COLUMN IF EXISTS legacy_pending_approval_id;
