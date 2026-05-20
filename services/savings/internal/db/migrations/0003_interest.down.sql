DROP TABLE IF EXISTS tax_payable_ledger;
DROP TABLE IF EXISTS interest_run_lines;
DROP TABLE IF EXISTS interest_runs;
DROP TYPE IF EXISTS interest_payout_method;
DROP TYPE IF EXISTS interest_run_status;

ALTER TABLE deposit_products DROP COLUMN IF EXISTS interest_eligible;
ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS default_interest_payout,
  DROP COLUMN IF EXISTS fy_start_day,
  DROP COLUMN IF EXISTS fy_start_month;
