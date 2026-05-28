-- Rollback Phase 4 collections extensions.
--
-- Net-new tables are dropped. ALTERed legacy tables retain their new
-- columns (loan_promises_to_pay.cancel_reason). The loan_doc_kind enum
-- additions cannot be removed from a Postgres ENUM; harmless.

DROP TABLE IF EXISTS dividend_offset_postings;
ALTER TABLE tenant_operations DROP COLUMN IF EXISTS dividend_offset_policy;

DROP TABLE IF EXISTS collections_message_templates;
DROP TABLE IF EXISTS collections_escalation_rules;
DROP TABLE IF EXISTS loan_assignment_history;
DROP TABLE IF EXISTS loan_collection_events;

DROP TYPE IF EXISTS collections_letter_kind;
DROP TYPE IF EXISTS loan_collection_event_kind;

ALTER TABLE loan_promises_to_pay DROP COLUMN IF EXISTS cancel_reason;
