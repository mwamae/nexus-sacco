DROP TRIGGER IF EXISTS trg_provision_run_lines_populate_counterparty ON provision_run_lines;
DROP INDEX IF EXISTS provision_run_lines_counterparty_idx;
ALTER TABLE provision_run_lines DROP COLUMN IF EXISTS counterparty_id;

DROP TRIGGER IF EXISTS trg_loan_writeoffs_populate_counterparty ON loan_writeoffs;
DROP INDEX IF EXISTS loan_writeoffs_counterparty_idx;
ALTER TABLE loan_writeoffs DROP COLUMN IF EXISTS counterparty_id;

DROP TRIGGER IF EXISTS trg_deposit_daily_balances_populate_counterparty ON deposit_daily_balances;
DROP INDEX IF EXISTS deposit_daily_balances_counterparty_idx;
ALTER TABLE deposit_daily_balances DROP COLUMN IF EXISTS counterparty_id;
