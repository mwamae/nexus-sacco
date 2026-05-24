-- Back-link from a loan_applications row to the workflow_instance
-- that owns its credit decision under the Unified Inbox cutover
-- (PR #4). Mirrors the pending_approvals.workflow_instance_id
-- pattern from PR #3.
--
-- Used for:
--   1. Idempotency on the submit-for-decision endpoint —
--      re-clicking the CTA returns the existing instance rather
--      than creating a duplicate.
--   2. Dispatcher dedup — when the workflow webhook fires into
--      POST /internal/v1/loan-applications/{id}/resolve, already-
--      terminal loan_applications.status short-circuits to 200
--      so redelivered callbacks can't re-stamp approved fields.
--   3. Frontend deep-link — the loan-app detail page shows the
--      current Inbox row via this back-link.

ALTER TABLE loan_applications
  ADD COLUMN IF NOT EXISTS workflow_instance_id uuid;

CREATE INDEX IF NOT EXISTS loan_apps_workflow_instance_idx
  ON loan_applications (workflow_instance_id)
  WHERE workflow_instance_id IS NOT NULL;
