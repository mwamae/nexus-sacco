-- Reverses migration 0050.

BEGIN;

DROP FUNCTION IF EXISTS find_comment_token_tenant(uuid);

DROP TABLE IF EXISTS loan_comment_templates;
DROP TABLE IF EXISTS loan_comments;
DROP TABLE IF EXISTS loan_application_score_history;

DROP INDEX IF EXISTS loan_documents_app_current_idx;
DROP INDEX IF EXISTS loan_documents_loan_current_idx;
DROP INDEX IF EXISTS loan_documents_expiring_idx;

ALTER TABLE loan_documents
  DROP COLUMN IF EXISTS superseded_by_id,
  DROP COLUMN IF EXISTS is_current,
  DROP COLUMN IF EXISTS review_notes,
  DROP COLUMN IF EXISTS reviewed_at,
  DROP COLUMN IF EXISTS reviewed_by,
  DROP COLUMN IF EXISTS review_status,
  DROP COLUMN IF EXISTS expires_at;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS document_expiry_warning_days,
  DROP COLUMN IF EXISTS document_expiry_windows,
  DROP COLUMN IF EXISTS default_required_document_kinds;

ALTER TABLE loan_products
  DROP COLUMN IF EXISTS required_document_kinds;

COMMIT;
