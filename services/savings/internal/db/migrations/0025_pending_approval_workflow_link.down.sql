DROP INDEX IF EXISTS pending_approvals_workflow_instance_idx;
ALTER TABLE pending_approvals DROP COLUMN IF EXISTS workflow_instance_id;
