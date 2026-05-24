DROP INDEX IF EXISTS loan_apps_workflow_instance_idx;
ALTER TABLE loan_applications DROP COLUMN IF EXISTS workflow_instance_id;
