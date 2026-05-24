-- Unified Inbox (PR #7): back-link from a journal_entries row to the
-- workflow instance gating its decision. The JE itself is the
-- proposal — no separate proposals table — because the existing
-- status enum (draft | pending_approval | posted | rejected) already
-- captures every lifecycle state we need.
--
-- Lifecycle when tenant.unified_inbox_enabled = true:
--   1. POST /v1/journal-entries (manual) → row inserted with
--      status='pending_approval' + workflow_instance_id set. Sub-
--      threshold entries auto-approve at creation (PR #2 seed
--      condition); the callback fires immediately and posts.
--   2. POST /v1/journal-entries/{id}/reverse → inverse-lines draft
--      inserted with status='pending_approval', reversal_of set,
--      + workflow_instance_id pointing at the journal_reversal
--      wf_instance.
--   3. POST /internal/v1/journal-entries/resolve →
--      on approve  → ApproveAndPostTx fires, status='posted'.
--      on reject   → RejectTx fires, status='rejected'.
--
-- The legacy inline Approve/Reject endpoints stay alive for tenants
-- without the flag.

ALTER TABLE journal_entries
  ADD COLUMN IF NOT EXISTS workflow_instance_id uuid;

CREATE INDEX IF NOT EXISTS journal_entries_workflow_instance_idx
  ON journal_entries (workflow_instance_id)
  WHERE workflow_instance_id IS NOT NULL;
