DROP INDEX IF EXISTS applications_materialized_org_idx;
ALTER TABLE membership_applications DROP COLUMN IF EXISTS materialized_org_id;
