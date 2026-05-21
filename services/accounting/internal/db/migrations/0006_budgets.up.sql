-- ═══════════════════════════════════════════════════════════════════
-- Budgeting (Phase 11/8).
--
-- A budget is a planned set of revenue + expense targets for a fiscal
-- year, broken down by account and month. Workflow:
--
--   draft → submitted → approved → archived
--
-- Once approved, lines become immutable. Only one budget per fiscal
-- year can be in 'approved' state at a time (the "active" budget for
-- variance reports). The previous approved budget must be archived
-- first.
--
-- Variance reporting compares posted P&L activity against budget_lines
-- for the same period — favourable when income exceeds budget OR
-- expense undershoots it.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS budgets (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name                    text NOT NULL,
  fiscal_year             int  NOT NULL,
  period_start            date NOT NULL,
  period_end              date NOT NULL,
  status                  text NOT NULL DEFAULT 'draft'
                           CHECK (status IN ('draft', 'submitted', 'approved', 'archived')),
  total_income_budget     numeric(18,2) NOT NULL DEFAULT 0,
  total_expense_budget    numeric(18,2) NOT NULL DEFAULT 0,
  net_surplus_budget      numeric(18,2) NOT NULL DEFAULT 0,
  notes                   text,
  submitted_at            timestamptz,
  submitted_by            uuid,
  approved_at             timestamptz,
  approved_by             uuid,
  archived_at             timestamptz,
  archived_by             uuid,
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid,
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CHECK (period_end > period_start)
);

CREATE INDEX IF NOT EXISTS budgets_tenant_idx ON budgets (tenant_id, fiscal_year DESC, status);
CREATE UNIQUE INDEX IF NOT EXISTS budgets_one_approved_per_year
  ON budgets (tenant_id, fiscal_year) WHERE status = 'approved';

ALTER TABLE budgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE budgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_budgets ON budgets
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON budgets TO nexus_app;


CREATE TABLE IF NOT EXISTS budget_lines (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  budget_id       uuid NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
  account_id      uuid NOT NULL REFERENCES chart_of_accounts(id) ON DELETE RESTRICT,
  account_code    text NOT NULL,
  account_class   text NOT NULL CHECK (account_class IN ('income','expense')),
  period_month    int  NOT NULL CHECK (period_month BETWEEN 1 AND 12),
  amount          numeric(18,2) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  notes           text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (budget_id, account_id, period_month)
);

CREATE INDEX IF NOT EXISTS budget_lines_budget_idx ON budget_lines (budget_id, account_class, account_code);

ALTER TABLE budget_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_budget_lines ON budget_lines
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON budget_lines TO nexus_app;
