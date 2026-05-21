-- ═══════════════════════════════════════════════════════════════════
-- Bank reconciliation (Phase 11/5).
--
-- Three tables that let finance match externally-supplied bank
-- statement lines to the corresponding GL journal lines on a cash
-- account, and flag the residual unmatched items so the GL balance
-- can be reconciled to the bank's balance.
--
--   bank_accounts          — one per real bank account, linked to a
--                            GL cash account code (1020/1030/1040/…)
--   bank_statements        — header per uploaded statement file
--   bank_statement_lines   — individual statement rows + match state
--
-- Matching links bank_statement_lines.matched_journal_line_id to a
-- journal_lines row. A successful match means: this bank txn IS the
-- GL txn. Unmatched on either side means an outstanding item that
-- needs follow-up (timing difference, bank charge to post, error).
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS bank_accounts (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  gl_account_code  text NOT NULL,
  bank_name        text NOT NULL,
  account_number   text NOT NULL,
  branch           text,
  currency_code    text NOT NULL DEFAULT 'KES',
  is_active        boolean NOT NULL DEFAULT true,
  notes            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, account_number)
);

CREATE INDEX IF NOT EXISTS bank_accounts_tenant_idx
  ON bank_accounts (tenant_id, is_active);
CREATE INDEX IF NOT EXISTS bank_accounts_gl_idx
  ON bank_accounts (tenant_id, gl_account_code);

ALTER TABLE bank_accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_accounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_bank_accounts ON bank_accounts
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON bank_accounts TO nexus_app;


CREATE TABLE IF NOT EXISTS bank_statements (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  bank_account_id   uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE CASCADE,
  statement_date    date NOT NULL,
  period_start      date,
  period_end        date,
  opening_balance   numeric(18,2),
  closing_balance   numeric(18,2),
  total_debits      numeric(18,2) NOT NULL DEFAULT 0,
  total_credits     numeric(18,2) NOT NULL DEFAULT 0,
  line_count        int NOT NULL DEFAULT 0,
  source_format     text NOT NULL DEFAULT 'csv',
  source_filename   text,
  uploaded_at       timestamptz NOT NULL DEFAULT now(),
  uploaded_by       uuid
);

CREATE INDEX IF NOT EXISTS bank_statements_account_idx
  ON bank_statements (bank_account_id, statement_date DESC);

ALTER TABLE bank_statements ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_statements FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_bank_statements ON bank_statements
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON bank_statements TO nexus_app;


CREATE TABLE IF NOT EXISTS bank_statement_lines (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  statement_id             uuid NOT NULL REFERENCES bank_statements(id) ON DELETE CASCADE,
  bank_account_id          uuid NOT NULL REFERENCES bank_accounts(id) ON DELETE CASCADE,
  line_no                  int  NOT NULL,
  txn_date                 date NOT NULL,
  value_date               date,
  description              text,
  reference                text,
  debit                    numeric(18,2) NOT NULL DEFAULT 0,   -- money out of bank account
  credit                   numeric(18,2) NOT NULL DEFAULT 0,   -- money in
  running_balance          numeric(18,2),
  match_status             text NOT NULL DEFAULT 'unmatched'
                            CHECK (match_status IN ('unmatched','matched','manual_match','excluded','adjusted')),
  matched_journal_line_id  uuid REFERENCES journal_lines(id) ON DELETE SET NULL,
  matched_at               timestamptz,
  matched_by               uuid,
  match_notes              text
);

CREATE INDEX IF NOT EXISTS bank_lines_statement_idx
  ON bank_statement_lines (statement_id, line_no);
CREATE INDEX IF NOT EXISTS bank_lines_status_idx
  ON bank_statement_lines (bank_account_id, match_status, txn_date);
CREATE UNIQUE INDEX IF NOT EXISTS bank_lines_journal_match_unique
  ON bank_statement_lines (matched_journal_line_id) WHERE matched_journal_line_id IS NOT NULL;

ALTER TABLE bank_statement_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE bank_statement_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_bank_statement_lines ON bank_statement_lines
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON bank_statement_lines TO nexus_app;
