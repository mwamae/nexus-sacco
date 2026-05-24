DROP INDEX IF EXISTS journal_entries_workflow_instance_idx;
ALTER TABLE journal_entries DROP COLUMN IF EXISTS workflow_instance_id;
