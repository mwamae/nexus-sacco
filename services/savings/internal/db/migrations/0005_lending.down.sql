ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS affordability_max_installment_pct_of_disposable,
  DROP COLUMN IF EXISTS affordability_dti_threshold_pct,
  DROP COLUMN IF EXISTS dpd_loss_days,
  DROP COLUMN IF EXISTS dpd_doubtful_days,
  DROP COLUMN IF EXISTS dpd_substandard_days,
  DROP COLUMN IF EXISTS loan_repayment_waterfall;

DROP TABLE IF EXISTS loan_transactions;
DROP TABLE IF EXISTS loan_repayment_schedule;
DROP TABLE IF EXISTS loans;
DROP TABLE IF EXISTS loan_documents;
DROP TABLE IF EXISTS loan_collateral;
DROP TABLE IF EXISTS loan_guarantees;
DROP TABLE IF EXISTS loan_applications;
DROP TABLE IF EXISTS loan_purpose_categories;
DROP TABLE IF EXISTS loan_products;

DROP TYPE IF EXISTS loan_employment_type;
DROP TYPE IF EXISTS loan_txn_type;
DROP TYPE IF EXISTS loan_status;
DROP TYPE IF EXISTS loan_doc_kind;
DROP TYPE IF EXISTS loan_collateral_kind;
DROP TYPE IF EXISTS loan_guarantee_status;
DROP TYPE IF EXISTS loan_application_status;
DROP TYPE IF EXISTS loan_multiplier_basis;
DROP TYPE IF EXISTS loan_collateral_requirement;
DROP TYPE IF EXISTS loan_fee_timing;
DROP TYPE IF EXISTS loan_repayment_method;
DROP TYPE IF EXISTS loan_interest_method;
DROP TYPE IF EXISTS loan_category;
