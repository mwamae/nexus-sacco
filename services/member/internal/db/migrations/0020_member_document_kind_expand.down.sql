DROP INDEX IF EXISTS member_documents_expiry_idx;
DROP INDEX IF EXISTS member_documents_cp_kind_singular_idx;
-- Restore the legacy unique (best-effort; the original name is
-- environment-dependent — applications relying on the old constraint
-- name must rename after the rollback).
ALTER TABLE member_documents
  ADD CONSTRAINT member_documents_counterparty_id_kind_key UNIQUE (counterparty_id, kind);
ALTER TABLE member_documents
  DROP COLUMN IF EXISTS verification_note,
  DROP COLUMN IF EXISTS verified_at,
  DROP COLUMN IF EXISTS verified_by,
  DROP COLUMN IF EXISTS verification,
  DROP COLUMN IF EXISTS expiry_date,
  DROP COLUMN IF EXISTS issue_date;
