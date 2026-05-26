-- Phase 3.5 — applier schema.
--
-- Adds the columns + table the finance executors and the mpesa
-- applier need:
--   • external_validation_ref on loan_transactions + deposit_transactions
--     so the receipt is traceable from any side
--   • member_fees_due (per-member fee outstandings table)
--
-- All additive. No destructive operations.

-- ─────────── external_validation_ref on transaction tables ───────────
ALTER TABLE loan_transactions
  ADD COLUMN IF NOT EXISTS external_validation_ref text;
ALTER TABLE deposit_transactions
  ADD COLUMN IF NOT EXISTS external_validation_ref text;

CREATE INDEX IF NOT EXISTS loan_transactions_external_ref_idx
  ON loan_transactions (external_validation_ref) WHERE external_validation_ref IS NOT NULL;
CREATE INDEX IF NOT EXISTS deposit_transactions_external_ref_idx
  ON deposit_transactions (external_validation_ref) WHERE external_validation_ref IS NOT NULL;

COMMENT ON COLUMN loan_transactions.external_validation_ref IS
  'Upstream rail receipt id (e.g. Safaricom MpesaReceiptNumber). When set the row was created by a Safaricom-validated rail and skipped the maker-checker approval queue. Owned by services/finance; consumed by services/mpesa applier + services/savings collection-desk router.';
COMMENT ON COLUMN deposit_transactions.external_validation_ref IS
  'See loan_transactions.external_validation_ref.';

-- ─────────── member_fees_due ───────────
-- Per-member fee outstandings. Populated by whichever module charged
-- the fee (loan disbursement adds processing fees here; the welfare
-- collection module adds welfare contributions; etc). The mpesa
-- engine queries it during plan-build via FeesDueTx; the finance fee
-- executor reduces amount_due on payment.
--
-- Status lifecycle:
--   open    — fee charged, not yet paid
--   partial — some paid, some still owed
--   paid    — fully settled
--   waived  — staff cancelled via the collection desk

CREATE TABLE IF NOT EXISTS member_fees_due (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id   uuid NOT NULL REFERENCES counterparties(id) ON DELETE CASCADE,
  fee_code          text NOT NULL,
  amount_due        numeric(18,2) NOT NULL,
  due_date          date,
  status            text NOT NULL DEFAULT 'open',
  source_module     text NOT NULL,        -- 'loan.disbursement','welfare','manual',…
  source_ref        text,                  -- upstream id (loan_no, etc) for traceability
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, counterparty_id, fee_code, source_module, source_ref)
);

CREATE INDEX IF NOT EXISTS member_fees_due_open_idx
  ON member_fees_due (tenant_id, counterparty_id, status, due_date)
  WHERE status IN ('open', 'partial');

-- RLS — same pattern as every other tenant-scoped table.
ALTER TABLE member_fees_due ENABLE ROW LEVEL SECURITY;
ALTER TABLE member_fees_due FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_member_fees_due ON member_fees_due
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON member_fees_due TO nexus_app;

COMMENT ON TABLE member_fees_due IS
  'Per-member fee outstandings. The mpesa distribution engine queries this for the fees_due waterfall leg; the finance fee executor reduces amount_due on payment.';
