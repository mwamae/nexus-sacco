BEGIN;
ALTER TABLE tenant_operations DROP COLUMN IF EXISTS writeoff_through_provision;
COMMIT;
