-- Back-link from a savings pending_approvals row to the
-- workflow_instance that the Unified Inbox cutover created from it.
--
-- Used for two distinct purposes:
--   1. Converter idempotency — the one-shot
--      cmd/migrate-cash-approvals run picks rows WHERE
--      workflow_instance_id IS NULL so it can be re-run safely
--      after fixing any mapping miss.
--   2. Dispatcher dedup — when the workflow engine fires its
--      webhook into POST /internal/v1/pending-approvals/{id}/resolve,
--      the savings handler treats already-terminal rows as a
--      no-op so a redelivered callback can't double-post a
--      transaction.
--
-- The FK is intentionally a plain uuid (not REFERENCES wf_instances)
-- because wf_instances lives in a different service's migration
-- tracker. Cross-service FK enforcement isn't possible without
-- coupling the two services' migrate runs, and the converter +
-- dispatcher both validate by lookup anyway.

ALTER TABLE pending_approvals
  ADD COLUMN IF NOT EXISTS workflow_instance_id uuid;

-- Partial index — only the rows that have been migrated benefit
-- from a lookup by this column. Keeps the index tight.
CREATE INDEX IF NOT EXISTS pending_approvals_workflow_instance_idx
  ON pending_approvals (workflow_instance_id)
  WHERE workflow_instance_id IS NOT NULL;
