-- Rollback Phase 5 lending extensions.

DROP TABLE IF EXISTS bosa_liens;
ALTER TABLE tenant_operations DROP COLUMN IF EXISTS bosa_lien_release_policy;

DROP TABLE IF EXISTS checkoff_batch_rows;
DROP TABLE IF EXISTS checkoff_batches;

DROP TRIGGER IF EXISTS loan_group_apportionment_sum_check ON loan_group_apportionment;
DROP FUNCTION IF EXISTS enforce_group_apportionment_sum();
DROP TABLE IF EXISTS loan_group_apportionment;
DROP TABLE IF EXISTS loan_group_officer_consents;

ALTER TABLE loans
  DROP COLUMN IF EXISTS insider_category,
  DROP COLUMN IF EXISTS is_insider;

ALTER TABLE loan_applications
  DROP COLUMN IF EXISTS insider_category,
  DROP COLUMN IF EXISTS is_insider,
  DROP COLUMN IF EXISTS group_income_source,
  DROP COLUMN IF EXISTS borrower_counterparty_id,
  DROP COLUMN IF EXISTS applicant_kind,
  DROP COLUMN IF EXISTS refinance_source_loan_ids,
  DROP COLUMN IF EXISTS parent_loan_id,
  DROP COLUMN IF EXISTS application_type;
