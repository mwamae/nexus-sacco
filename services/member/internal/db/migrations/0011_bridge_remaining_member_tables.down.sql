DROP TRIGGER IF EXISTS trg_member_status_proposals_populate_counterparty ON member_status_proposals;
DROP INDEX IF EXISTS member_status_proposals_counterparty_idx;
ALTER TABLE member_status_proposals DROP COLUMN IF EXISTS counterparty_id;

DROP TRIGGER IF EXISTS trg_member_status_changes_populate_counterparty ON member_status_changes;
DROP INDEX IF EXISTS member_status_changes_counterparty_idx;
ALTER TABLE member_status_changes DROP COLUMN IF EXISTS counterparty_id;

DROP TRIGGER IF EXISTS trg_member_relations_populate_counterparty ON member_relations;
DROP INDEX IF EXISTS member_relations_counterparty_idx;
ALTER TABLE member_relations DROP COLUMN IF EXISTS counterparty_id;

DROP TRIGGER IF EXISTS trg_member_documents_populate_counterparty ON member_documents;
DROP INDEX IF EXISTS member_documents_counterparty_idx;
ALTER TABLE member_documents DROP COLUMN IF EXISTS counterparty_id;

-- populate_counterparty_id_from_member is owned by savings 0018; do not drop here.
