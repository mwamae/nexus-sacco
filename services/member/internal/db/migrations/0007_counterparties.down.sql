-- Drop in reverse-dependency order. Safe because Phase A doesn't
-- redirect any FK; legacy rows are untouched.
ALTER TABLE org_members DROP COLUMN IF EXISTS counterparty_id;
ALTER TABLE members     DROP COLUMN IF EXISTS counterparty_id;
DROP TABLE IF EXISTS counterparties;
DROP TYPE  IF EXISTS counterparty_risk_band;
DROP TYPE  IF EXISTS counterparty_kyc_state;
DROP TYPE  IF EXISTS counterparty_status;
DROP TYPE  IF EXISTS counterparty_kind;
