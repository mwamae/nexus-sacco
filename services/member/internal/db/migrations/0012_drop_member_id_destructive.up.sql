BEGIN;
-- Phase D sub-PR 2a — drop member_id columns from the four
-- member-owned tables bridged by migration 0011. counterparty_id is
-- now the canonical FK; the BEFORE INSERT trigger is no longer needed
-- because Go INSERTs pass counterparty_id directly (via the
-- ResolveCounterpartyID helper at the store boundary).

-- ─── member_documents ────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_member_documents_populate_counterparty ON member_documents;
DROP INDEX IF EXISTS member_documents_member_idx;
ALTER TABLE member_documents DROP CONSTRAINT IF EXISTS member_documents_member_id_kind_key;
ALTER TABLE member_documents DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS member_documents_counterparty_idx;
ALTER TABLE member_documents ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS member_documents_counterparty_idx ON member_documents(counterparty_id);
CREATE UNIQUE INDEX IF NOT EXISTS member_documents_counterparty_id_kind_key
  ON member_documents(counterparty_id, kind);

-- ─── member_relations ────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_member_relations_populate_counterparty ON member_relations;
DROP INDEX IF EXISTS member_relations_member_idx;
ALTER TABLE member_relations DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS member_relations_counterparty_idx;
ALTER TABLE member_relations ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX member_relations_counterparty_idx
  ON member_relations(counterparty_id, kind, "position");

-- ─── member_status_changes ───────────────────────────────────
DROP TRIGGER IF EXISTS trg_member_status_changes_populate_counterparty ON member_status_changes;
DROP INDEX IF EXISTS member_status_changes_member_idx;
ALTER TABLE member_status_changes DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS member_status_changes_counterparty_idx;
ALTER TABLE member_status_changes ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX member_status_changes_counterparty_idx
  ON member_status_changes(counterparty_id, changed_at DESC);

-- ─── member_status_proposals ─────────────────────────────────
DROP TRIGGER IF EXISTS trg_member_status_proposals_populate_counterparty ON member_status_proposals;
DROP INDEX IF EXISTS member_status_proposals_member_idx;
ALTER TABLE member_status_proposals DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS member_status_proposals_counterparty_idx;
ALTER TABLE member_status_proposals ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX member_status_proposals_counterparty_idx
  ON member_status_proposals(counterparty_id) WHERE (resolved_at IS NULL);

-- The savings service owns the trigger function and drops it (CASCADE)
-- in its own migration 0021. No-op here.

COMMIT;
