-- Backref column for the legacy-approvals-migrate backfill.
--
-- When the script runs (services/savings/cmd/legacy-approvals-migrate/
-- main.go --apply), each in-flight pending_approvals row becomes a
-- wf_instances row; the new wf row stores the source pa.id here so
-- the two trails join cleanly for audit + ops queries:
--
--   SELECT pa.id, pa.kind, wi.id, wi.process_kind, wi.status
--     FROM pending_approvals pa
--     LEFT JOIN wf_instances wi ON wi.legacy_pending_approval_id = pa.id
--    WHERE pa.status = 'migrated';
--
-- Nullable — non-backfilled instances (anything created via
-- workflowclient.CreateInstanceTx from a savings handler) leave it
-- NULL. The index is partial on NOT NULL so the cardinality stays
-- tiny (one entry per migrated pa).

ALTER TABLE wf_instances
  ADD COLUMN IF NOT EXISTS legacy_pending_approval_id uuid;

CREATE INDEX IF NOT EXISTS wf_instances_legacy_pa_idx
  ON wf_instances (legacy_pending_approval_id)
  WHERE legacy_pending_approval_id IS NOT NULL;

COMMENT ON COLUMN wf_instances.legacy_pending_approval_id IS
  'Source pending_approvals.id for instances created by the legacy-approvals-migrate backfill. NULL for natively-created instances.';
