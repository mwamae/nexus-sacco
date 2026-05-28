-- Rollback Phase 6 CRB + insurance + member portal flag.
--
-- ENUM values cannot be removed from a Postgres ENUM type; the type
-- itself is dropped after the dependent tables go.

ALTER TABLE members DROP COLUMN IF EXISTS has_self_service;

DROP TABLE IF EXISTS loan_insurance_policies;

ALTER TABLE loan_products
  DROP COLUMN IF EXISTS insurance_mandatory,
  DROP COLUMN IF EXISTS insurance_provider_id;

DROP TABLE IF EXISTS insurance_providers;
DROP TABLE IF EXISTS crb_pulls;
DROP TABLE IF EXISTS crb_credentials;

DROP TYPE IF EXISTS crb_provider;
