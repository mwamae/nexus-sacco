-- Preamble for Phase D sub-PR 2 — bridge the last three savings
-- tables that still key off members.id without a matching
-- counterparty_id column. Re-applies migration 0018's playbook:
-- additive uuid column, partial index, one-shot backfill from
-- members.counterparty_id, BEFORE INSERT trigger that auto-fills
-- on new rows. Idempotent across re-runs only at the
-- ALTER/CREATE level — the UPDATE is naturally idempotent because
-- the WHERE counterparty_id IS NULL clause guards repeat writes.
--
-- The three covered tables (and why each matters):
--   deposit_daily_balances (367 rows) — per-account daily snapshot
--     used by the dividend pro-rata calculator. Read-heavy but the
--     row count is bounded by accounts × days, so the column add
--     is cheap.
--   loan_writeoffs (2 rows so far) — write-off accounting records.
--   provision_run_lines (3 rows) — per-loan IFRS-9 stage allocations
--     produced by each provisioning run.
--
-- Note on cross-service safety: the trigger function
-- populate_counterparty_id_from_member was created by migration
-- 0018; this migration references it but does not redefine it
-- because savings always owns the function. The member &
-- notification preambles use CREATE OR REPLACE to stay
-- self-contained.

ALTER TABLE deposit_daily_balances
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX deposit_daily_balances_counterparty_idx
  ON deposit_daily_balances(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE deposit_daily_balances dbal
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE dbal.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_deposit_daily_balances_populate_counterparty
  BEFORE INSERT ON deposit_daily_balances
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE loan_writeoffs
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX loan_writeoffs_counterparty_idx
  ON loan_writeoffs(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE loan_writeoffs lw
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE lw.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_loan_writeoffs_populate_counterparty
  BEFORE INSERT ON loan_writeoffs
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE provision_run_lines
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX provision_run_lines_counterparty_idx
  ON provision_run_lines(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE provision_run_lines prl
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE prl.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_provision_run_lines_populate_counterparty
  BEFORE INSERT ON provision_run_lines
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
