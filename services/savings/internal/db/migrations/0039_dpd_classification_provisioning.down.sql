-- Rollback Phase 3 DPD / classification / provisioning schema.
--
-- Drops in reverse dependency order. ALTERed columns on existing
-- tables are removed; existing rows in provision_runs / provision_run_lines
-- survive (Phase 3 fields revert to NULL semantics).

BEGIN;

-- provision_run_lines Phase 3 extensions
ALTER TABLE provision_run_lines
  DROP COLUMN IF EXISTS delta,
  DROP COLUMN IF EXISTS classification_ifrs9_stage,
  DROP COLUMN IF EXISTS product_id;

-- provision_runs Phase 3 extensions
DROP INDEX IF EXISTS provision_runs_tenant_month_unique;
ALTER TABLE provision_runs
  DROP COLUMN IF EXISTS journal_entry_id,
  DROP COLUMN IF EXISTS cancel_reason,
  DROP COLUMN IF EXISTS cancelled_by,
  DROP COLUMN IF EXISTS cancelled_at,
  DROP COLUMN IF EXISTS period_month;

-- tenant_operations DPD threshold columns
ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS ifrs9_stage3_dpd,
  DROP COLUMN IF EXISTS ifrs9_stage2_dpd,
  DROP COLUMN IF EXISTS sasra_watch_dpd;

DROP TABLE IF EXISTS ecl_rate_matrix;
DROP TABLE IF EXISTS loan_classification_history;
DROP TABLE IF EXISTS loan_dpd_snapshots;

COMMIT;
