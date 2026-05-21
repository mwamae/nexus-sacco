-- ═══════════════════════════════════════════════════════════════════
-- Fiscal year close audit trail (Phase 11/4).
--
-- Year-end close generates a single closing journal entry that zeros
-- every income/expense account into Retained Earnings (3010) and locks
-- all 12 monthly periods of the year. This table records the
-- closing journal + audit metadata so finance can answer "when did
-- FY 2025 close, by whom, with what surplus?" without re-querying
-- journal_entries.
--
-- UNIQUE (tenant_id, year) — a year can only be closed once. Re-opening
-- requires posting a reversal entry and deleting this row (admin-only).
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS fiscal_year_closes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  year               int  NOT NULL,
  fy_start           date NOT NULL,
  fy_end             date NOT NULL,
  closing_entry_id   uuid NOT NULL REFERENCES journal_entries(id) ON DELETE RESTRICT,
  total_income       numeric(18,2) NOT NULL,
  total_expense      numeric(18,2) NOT NULL,
  net_surplus        numeric(18,2) NOT NULL,
  income_accounts    int NOT NULL,
  expense_accounts   int NOT NULL,
  closed_at          timestamptz NOT NULL DEFAULT now(),
  closed_by          uuid NOT NULL,
  notes              text,
  UNIQUE (tenant_id, year)
);

CREATE INDEX IF NOT EXISTS fiscal_year_closes_tenant_idx
  ON fiscal_year_closes (tenant_id, year DESC);

ALTER TABLE fiscal_year_closes ENABLE ROW LEVEL SECURITY;
ALTER TABLE fiscal_year_closes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_fiscal_year_closes ON fiscal_year_closes
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON fiscal_year_closes TO nexus_app;
