-- Extend pending_approvals.status to allow 'migrated' — the terminal
-- state the legacy-approvals-migrate backfill stamps after creating
-- a wf_instance for the row.
--
-- The CHECK constraint in 0010 only allowed the five legacy states;
-- without this extension the backfill's UPDATE would fail on every
-- row. ALTER TABLE … DROP CONSTRAINT … ADD CONSTRAINT in one
-- statement keeps the table consistent during the migration window.
--
-- 'migrated' is intentionally a terminal-class state — the row stays
-- visible for audit + drives the wf_instances_legacy_pa_idx join,
-- but UI list queries already filter status IN ('pending') so a
-- migrated row drops out of the maker's queue without a separate
-- WHERE clause change.

ALTER TABLE pending_approvals
  DROP CONSTRAINT IF EXISTS pending_approvals_status_check;

ALTER TABLE pending_approvals
  ADD CONSTRAINT pending_approvals_status_check
  CHECK (status IN ('pending','approved','declined','cancelled','execution_error','migrated'));
