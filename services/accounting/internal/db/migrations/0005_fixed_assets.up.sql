-- ═══════════════════════════════════════════════════════════════════
-- Fixed Assets management (Phase 11/7).
--
-- The asset register tracks every long-lived asset (furniture,
-- equipment, computers, vehicles, buildings, land, intangibles) from
-- acquisition through depreciation to disposal. Each asset stores its
-- own GL account triplet (gross / accumulated dep / depreciation
-- expense) so the depreciation engine can post against the right
-- accounts even when the CoA mapping varies per asset class.
--
-- Acquisition auto-posts:    DR gross_asset / CR funded_from
-- Monthly depreciation:      DR dep_expense / CR accumulated_dep
-- Disposal:
--   DR accumulated_dep                                     (eliminate)
--   DR cash/bank (proceeds, if any)                        (receive)
--   DR loss_on_disposal (if proceeds < book_value)
--   CR gross_asset                                         (eliminate)
--   CR gain_on_disposal (if proceeds > book_value)
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS fixed_assets (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  asset_no                    text NOT NULL,
  name                        text NOT NULL,
  description                 text,
  category                    text NOT NULL,                          -- 'furniture','equipment','computer','vehicle','building','land','intangible'
  gl_asset_code               text NOT NULL,                          -- 1500/1510/1520/1530/1540/1550/1600
  gl_accumulated_code         text NOT NULL DEFAULT '1590',
  gl_expense_code             text NOT NULL DEFAULT '5200',
  purchase_date               date NOT NULL,
  purchase_cost               numeric(18,2) NOT NULL CHECK (purchase_cost >= 0),
  salvage_value               numeric(18,2) NOT NULL DEFAULT 0 CHECK (salvage_value >= 0),
  useful_life_months          int NOT NULL DEFAULT 0 CHECK (useful_life_months >= 0),
  depreciation_method         text NOT NULL DEFAULT 'straight_line'
                               CHECK (depreciation_method IN ('straight_line', 'none')),
  location                    text,
  custodian                   text,
  supplier                    text,
  invoice_ref                 text,
  acquisition_journal_entry_id uuid REFERENCES journal_entries(id) ON DELETE SET NULL,
  status                      text NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active','disposed','written_off','fully_depreciated')),
  accumulated_depreciation    numeric(18,2) NOT NULL DEFAULT 0,
  last_depreciation_date      date,                                   -- end of last depreciated month
  disposal_journal_entry_id   uuid REFERENCES journal_entries(id) ON DELETE SET NULL,
  disposal_proceeds           numeric(18,2),
  disposal_gain_loss          numeric(18,2),
  disposed_at                 timestamptz,
  disposed_by                 uuid,
  notes                       text,
  created_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid,
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, asset_no)
);

CREATE INDEX IF NOT EXISTS fixed_assets_status_idx ON fixed_assets (tenant_id, status, category);
CREATE INDEX IF NOT EXISTS fixed_assets_purchase_idx ON fixed_assets (tenant_id, purchase_date DESC);

ALTER TABLE fixed_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE fixed_assets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_fixed_assets ON fixed_assets
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON fixed_assets TO nexus_app;


CREATE TABLE IF NOT EXISTS depreciation_runs (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  as_of_date          date NOT NULL,
  period_year         int  NOT NULL,
  period_month        int  NOT NULL,
  status              text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','computed','posted','failed','superseded')),
  assets_processed    int  NOT NULL DEFAULT 0,
  total_depreciation  numeric(18,2) NOT NULL DEFAULT 0,
  journal_entry_id    uuid REFERENCES journal_entries(id) ON DELETE SET NULL,
  notes               text,
  computed_at         timestamptz,
  posted_at           timestamptz,
  posted_by           uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid,
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS depreciation_runs_tenant_idx ON depreciation_runs (tenant_id, as_of_date DESC);
CREATE UNIQUE INDEX IF NOT EXISTS depreciation_runs_posted_unique
  ON depreciation_runs (tenant_id, period_year, period_month) WHERE status = 'posted';

ALTER TABLE depreciation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE depreciation_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_depreciation_runs ON depreciation_runs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON depreciation_runs TO nexus_app;


CREATE TABLE IF NOT EXISTS depreciation_run_lines (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id               uuid NOT NULL REFERENCES depreciation_runs(id) ON DELETE CASCADE,
  asset_id             uuid NOT NULL REFERENCES fixed_assets(id) ON DELETE RESTRICT,
  asset_no             text NOT NULL,
  asset_name           text NOT NULL,
  category             text NOT NULL,
  method               text NOT NULL,
  cost                 numeric(18,2) NOT NULL,
  salvage              numeric(18,2) NOT NULL,
  accumulated_before   numeric(18,2) NOT NULL,
  depreciation_amount  numeric(18,2) NOT NULL,
  accumulated_after    numeric(18,2) NOT NULL,
  book_value_after     numeric(18,2) NOT NULL,
  months_depreciated   int NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS depreciation_run_lines_run_idx ON depreciation_run_lines (run_id);

ALTER TABLE depreciation_run_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE depreciation_run_lines FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_depreciation_run_lines ON depreciation_run_lines
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON depreciation_run_lines TO nexus_app;
