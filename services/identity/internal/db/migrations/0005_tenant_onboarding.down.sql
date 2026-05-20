DROP TABLE IF EXISTS tenant_contacts;
DROP TABLE IF EXISTS tenant_branches;
DROP TYPE  IF EXISTS branch_kind;

ALTER TABLE tenants
  DROP COLUMN IF EXISTS billing_plan,
  DROP COLUMN IF EXISTS tax_pin,
  DROP COLUMN IF EXISTS registration_no;

DROP TYPE IF EXISTS billing_plan;
