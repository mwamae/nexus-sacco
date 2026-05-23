BEGIN;
-- Phase D sub-PR 2a — drop member_id columns from every savings table
-- bridged by migration 0018 + 0020. counterparty_id becomes the
-- canonical FK; the BEFORE INSERT trigger function is no longer needed
-- because Go INSERTs now pass counterparty_id directly.
--
-- Steps per table (uniform):
--   1. DROP TRIGGER       (was inserting counterparty_id from member_id)
--   2. DROP INDEX         (member_id indexes — read paths now hit counterparty_id)
--   3. ALTER TABLE DROP COLUMN member_id (cascades the FK to members(id))
--   4. DROP INDEX <table>_counterparty_idx (the partial idx WHERE NOT NULL)
--   5. ALTER COLUMN counterparty_id SET NOT NULL
--   6. CREATE INDEX <table>_counterparty_idx (full, no WHERE)
--   7. CREATE UNIQUE INDEX <table>_tenant_id_counterparty_id_key (where applicable)
--
-- After the per-table block, the populate_counterparty_id_from_member
-- function is dropped — nothing references it anymore.

-- ─── share_accounts ───────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_share_accounts_populate_counterparty ON share_accounts;
DROP INDEX IF EXISTS share_accounts_member_idx;
ALTER TABLE share_accounts DROP CONSTRAINT IF EXISTS share_accounts_tenant_id_member_id_key;
ALTER TABLE share_accounts DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS share_accounts_counterparty_idx;
ALTER TABLE share_accounts ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS share_accounts_counterparty_idx ON share_accounts(counterparty_id);
CREATE UNIQUE INDEX IF NOT EXISTS share_accounts_tenant_id_counterparty_id_key
  ON share_accounts(tenant_id, counterparty_id);

-- ─── share_transactions ──────────────────────────────────────
DROP TRIGGER IF EXISTS trg_share_transactions_populate_counterparty ON share_transactions;
DROP INDEX IF EXISTS share_txn_member_idx;
ALTER TABLE share_transactions DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS share_transactions_counterparty_idx;
ALTER TABLE share_transactions ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX share_txn_counterparty_idx
  ON share_transactions(counterparty_id, posted_at DESC);

-- ─── share_certificates ──────────────────────────────────────
DROP TRIGGER IF EXISTS trg_share_certificates_populate_counterparty ON share_certificates;
ALTER TABLE share_certificates DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS share_certificates_counterparty_idx;
ALTER TABLE share_certificates ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS share_certificates_counterparty_idx ON share_certificates(counterparty_id);

-- ─── deposit_accounts ────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_deposit_accounts_populate_counterparty ON deposit_accounts;
DROP INDEX IF EXISTS deposit_accounts_member_idx;
ALTER TABLE deposit_accounts DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS deposit_accounts_counterparty_idx;
ALTER TABLE deposit_accounts ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS deposit_accounts_counterparty_idx ON deposit_accounts(counterparty_id);

-- ─── deposit_transactions ────────────────────────────────────
DROP TRIGGER IF EXISTS trg_deposit_transactions_populate_counterparty ON deposit_transactions;
DROP INDEX IF EXISTS deposit_txn_member_posted_idx;
ALTER TABLE deposit_transactions DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS deposit_transactions_counterparty_idx;
ALTER TABLE deposit_transactions ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX deposit_txn_counterparty_posted_idx
  ON deposit_transactions(counterparty_id, posted_at DESC);

-- ─── deposit_daily_balances ──────────────────────────────────
DROP TRIGGER IF EXISTS trg_deposit_daily_balances_populate_counterparty ON deposit_daily_balances;
ALTER TABLE deposit_daily_balances DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS deposit_daily_balances_counterparty_idx;
ALTER TABLE deposit_daily_balances ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX deposit_daily_balances_counterparty_idx
  ON deposit_daily_balances(counterparty_id);

-- ─── loans ────────────────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_loans_populate_counterparty ON loans;
DROP INDEX IF EXISTS loans_member_idx;
ALTER TABLE loans DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS loans_counterparty_idx;
ALTER TABLE loans ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS loans_counterparty_idx ON loans(counterparty_id, status);

-- ─── loan_applications ───────────────────────────────────────
DROP TRIGGER IF EXISTS trg_loan_applications_populate_counterparty ON loan_applications;
DROP INDEX IF EXISTS loan_apps_member_idx;
ALTER TABLE loan_applications DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS loan_applications_counterparty_idx;
ALTER TABLE loan_applications ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX loan_apps_counterparty_idx
  ON loan_applications(counterparty_id, created_at DESC);

-- ─── loan_transactions ───────────────────────────────────────
DROP TRIGGER IF EXISTS trg_loan_transactions_populate_counterparty ON loan_transactions;
DROP INDEX IF EXISTS loan_txn_member_idx;
ALTER TABLE loan_transactions DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS loan_transactions_counterparty_idx;
ALTER TABLE loan_transactions ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX loan_txn_counterparty_idx
  ON loan_transactions(counterparty_id, posted_at DESC);

-- ─── loan_guarantees (special: guarantor_member_id, not member_id) ─
DROP INDEX IF EXISTS loan_guarantees_member_idx;
ALTER TABLE loan_guarantees DROP COLUMN IF EXISTS guarantor_member_id;
DROP INDEX IF EXISTS loan_guarantees_counterparty_idx;
ALTER TABLE loan_guarantees ALTER COLUMN guarantor_counterparty_id SET NOT NULL;
CREATE INDEX loan_guarantees_guarantor_counterparty_idx
  ON loan_guarantees(guarantor_counterparty_id, status);

-- ─── loan_collection_cases ───────────────────────────────────
ALTER TABLE loan_collection_cases DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS loan_collection_cases_counterparty_idx;
ALTER TABLE loan_collection_cases ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX loan_collection_cases_counterparty_idx
  ON loan_collection_cases(counterparty_id);

-- ─── loan_writeoffs ──────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_loan_writeoffs_populate_counterparty ON loan_writeoffs;
ALTER TABLE loan_writeoffs DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS loan_writeoffs_counterparty_idx;
ALTER TABLE loan_writeoffs ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX IF NOT EXISTS loan_writeoffs_counterparty_idx ON loan_writeoffs(counterparty_id);

-- ─── dividend_run_lines ──────────────────────────────────────
DROP INDEX IF EXISTS dividend_run_lines_member_idx;
ALTER TABLE dividend_run_lines DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS dividend_run_lines_counterparty_idx;
ALTER TABLE dividend_run_lines ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX dividend_run_lines_counterparty_idx
  ON dividend_run_lines(counterparty_id, run_id);

-- ─── interest_run_lines ──────────────────────────────────────
DROP INDEX IF EXISTS interest_run_lines_member_idx;
ALTER TABLE interest_run_lines DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS interest_run_lines_counterparty_idx;
ALTER TABLE interest_run_lines ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX interest_run_lines_counterparty_idx
  ON interest_run_lines(counterparty_id, run_id);

-- ─── tax_payable_ledger ──────────────────────────────────────
DROP TRIGGER IF EXISTS trg_tax_payable_ledger_populate_counterparty ON tax_payable_ledger;
DROP INDEX IF EXISTS tax_payable_member_idx;
ALTER TABLE tax_payable_ledger DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS tax_payable_ledger_counterparty_idx;
ALTER TABLE tax_payable_ledger ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX tax_payable_counterparty_idx
  ON tax_payable_ledger(counterparty_id, posted_at DESC);

-- ─── provision_run_lines ─────────────────────────────────────
DROP TRIGGER IF EXISTS trg_provision_run_lines_populate_counterparty ON provision_run_lines;
ALTER TABLE provision_run_lines DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS provision_run_lines_counterparty_idx;
ALTER TABLE provision_run_lines ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX provision_run_lines_counterparty_idx
  ON provision_run_lines(counterparty_id);

-- ─── Trigger function cleanup ─────────────────────────────────
-- CASCADE so any cross-service triggers still attached (member 0012,
-- notification 0013 might land before this migration) get cleaned up
-- in lockstep. The member/notification migrations issue their own
-- DROP TRIGGER IF EXISTS, so the cascade overlap is harmless either
-- way.
DROP FUNCTION IF EXISTS populate_counterparty_id_from_member() CASCADE;

COMMIT;
