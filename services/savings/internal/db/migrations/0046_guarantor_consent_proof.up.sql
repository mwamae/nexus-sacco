-- Phase 5 follow-up — admin-captured guarantor consent.
--
-- Adds the storage shape needed for two workflows:
--
--   1. Admin captures consent on the guarantor's behalf and uploads
--      a signed consent document (PDF / image) as proof. The
--      document lives in loan_documents tied to the application;
--      loan_guarantees.proof_document_id points at it for fast
--      lookup from the consent UI.
--
--   2. Member self-serves via the member portal. No file is uploaded
--      (the JWT identifies the consenting member); but responded_by
--      still records who clicked.
--
-- responded_by was missing. The existing responded_at told you WHEN
-- but not WHO — bad audit signal once admin-captured consent lands.

ALTER TYPE loan_doc_kind ADD VALUE IF NOT EXISTS 'guarantor_consent_proof';

ALTER TABLE loan_guarantees
  ADD COLUMN IF NOT EXISTS proof_document_id uuid REFERENCES loan_documents(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS responded_by      uuid;

CREATE INDEX IF NOT EXISTS loan_guarantees_proof_doc_idx
  ON loan_guarantees (proof_document_id) WHERE proof_document_id IS NOT NULL;

COMMENT ON COLUMN loan_guarantees.proof_document_id IS
  'Phase 5 — uploaded consent proof (signed form / scanned ID). Set when an admin captures consent on the guarantor''s behalf. Null for member-self-service consent (the JWT is the proof).';
COMMENT ON COLUMN loan_guarantees.responded_by IS
  'Phase 5 — user.id who recorded the response. For admin-captured: the officer. For member self-service: the member''s user account.';
