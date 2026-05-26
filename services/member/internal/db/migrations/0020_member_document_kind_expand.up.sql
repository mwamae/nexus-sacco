-- member_documents — KYC workstation columns + partial unique.
--
-- Mirrors org_documents (issue_date, expiry_date, verification,
-- verified_by, verified_at, verification_note) so the individual-side
-- KYC tab can carry the same metadata as the institutional side.
--
-- The existing UNIQUE (counterparty_id, kind) is replaced with a
-- partial unique that allows multiple rows for kind='other' — the
-- only kind that legitimately repeats (e.g. multiple supplementary
-- documents under "other / supporting"). Fixed kinds (id_front,
-- passport_photo, etc) remain singleton per counterparty via the
-- partial index's WHERE clause.
--
-- Depends on migration 0019 for the 'other' enum value.

ALTER TABLE member_documents
  ADD COLUMN IF NOT EXISTS issue_date         date,
  ADD COLUMN IF NOT EXISTS expiry_date        date,
  ADD COLUMN IF NOT EXISTS verification       doc_verification NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS verified_by        uuid,
  ADD COLUMN IF NOT EXISTS verified_at        timestamptz,
  ADD COLUMN IF NOT EXISTS verification_note  text;

-- Drop legacy unique constraints (the index name has varied across
-- migrations — check both candidates so this is reproducible).
ALTER TABLE member_documents DROP CONSTRAINT IF EXISTS member_documents_member_id_kind_key;
ALTER TABLE member_documents DROP CONSTRAINT IF EXISTS member_documents_counterparty_id_kind_key;

-- Partial unique: singleton per (cp, kind) for every kind EXCEPT
-- 'other'. The typed enum comparison is immutable; ::text would
-- be only stable and rejected by Postgres for index predicates.
CREATE UNIQUE INDEX IF NOT EXISTS member_documents_cp_kind_singular_idx
  ON member_documents (counterparty_id, kind)
  WHERE kind <> 'other'::document_kind;

-- Hot-path index for the KYC dashboard expiry chip + the future
-- janitor that flags expiring documents.
CREATE INDEX IF NOT EXISTS member_documents_expiry_idx
  ON member_documents (tenant_id, expiry_date) WHERE expiry_date IS NOT NULL;
