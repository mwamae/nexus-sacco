-- Postgres can't drop ENUM values; the loan_doc_kind addition stays.
DROP INDEX IF EXISTS loan_guarantees_proof_doc_idx;
ALTER TABLE loan_guarantees
  DROP COLUMN IF EXISTS proof_document_id,
  DROP COLUMN IF EXISTS responded_by;
