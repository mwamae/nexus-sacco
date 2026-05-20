-- Tenant onboarding fields. Adds:
--   * registration_no  — company / coop registration number
--   * tax_pin          — government tax id (KRA PIN in Kenya)
--   * billing_plan     — which plan the SACCO is on
--   * tenant_branches  — HQ + branch network
--   * tenant_contacts  — primary contact persons (CEO, COO, ops manager…)

CREATE TYPE billing_plan AS ENUM ('starter', 'standard', 'premium', 'enterprise');
CREATE TYPE branch_kind  AS ENUM ('hq', 'branch', 'agency');

ALTER TABLE tenants
  ADD COLUMN registration_no text,
  ADD COLUMN tax_pin         text,
  ADD COLUMN billing_plan    billing_plan NOT NULL DEFAULT 'starter';

CREATE TABLE tenant_branches (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code             text NOT NULL,                -- "HQ", "NK-01", short id
  name             text NOT NULL,
  kind             branch_kind NOT NULL DEFAULT 'branch',
  county           text,
  sub_county       text,
  physical_address text,
  phone            text,
  position         int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);
CREATE TRIGGER tenant_branches_updated_at BEFORE UPDATE ON tenant_branches
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX tenant_branches_tenant_idx ON tenant_branches (tenant_id, position);

CREATE TABLE tenant_contacts (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  full_name   text NOT NULL,
  title       text,                              -- "CEO", "Operations Manager", …
  email       citext,
  phone       text,
  position    int NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tenant_contacts_tenant_idx ON tenant_contacts (tenant_id, position);

-- RLS — both tables are tenant-scoped; only the owner tenant sees rows.
ALTER TABLE tenant_branches ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_branches FORCE  ROW LEVEL SECURITY;
ALTER TABLE tenant_contacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_contacts FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_branches ON tenant_branches
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_contacts ON tenant_contacts
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_branches, tenant_contacts TO nexus_app;
