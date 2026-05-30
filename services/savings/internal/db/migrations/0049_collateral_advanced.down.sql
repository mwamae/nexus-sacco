-- Down — reverse dependency order. Restores loan_collateral to its
-- post-Phase-1.5a state.

BEGIN;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS collateral_insurance_warning_days,
  DROP COLUMN IF EXISTS collateral_revaluation_warning_days,
  DROP COLUMN IF EXISTS collateral_insurance_required_kinds,
  DROP COLUMN IF EXISTS collateral_charge_required_kinds;

DROP FUNCTION IF EXISTS find_pledger_token_tenant(bytea);
DROP TABLE IF EXISTS collateral_pledger_consent_tokens;
DROP TABLE IF EXISTS collateral_auction_events;
DROP TABLE IF EXISTS collateral_document_custody;
DROP TABLE IF EXISTS collateral_insurance_policies;

ALTER TABLE loan_collateral
  DROP COLUMN IF EXISTS charge_certificate_path,
  DROP COLUMN IF EXISTS charge_discharged_at,
  DROP COLUMN IF EXISTS charge_discharge_ref,
  DROP COLUMN IF EXISTS charge_registered_by,
  DROP COLUMN IF EXISTS charge_registered_at,
  DROP COLUMN IF EXISTS charge_reference,
  DROP COLUMN IF EXISTS charge_registry;

DROP TYPE IF EXISTS charge_registry;

DROP TABLE IF EXISTS collateral_share_pledges;
DROP TABLE IF EXISTS collateral_deposit_liens;

DROP INDEX IF EXISTS loan_collateral_pledger_idx;
ALTER TABLE loan_collateral
  DROP COLUMN IF EXISTS pledger_consent_doc_path,
  DROP COLUMN IF EXISTS pledger_consent_at,
  DROP COLUMN IF EXISTS pledger_consent_status,
  DROP COLUMN IF EXISTS pledger_counterparty_id;

COMMIT;
