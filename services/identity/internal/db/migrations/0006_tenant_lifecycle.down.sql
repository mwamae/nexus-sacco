-- Down migration is destructive: any tenant in a new status (trial,
-- expired, pending_setup, archived → previously closed only) collapses
-- back to active or suspended. Use only in development.

CREATE TYPE tenant_status_legacy AS ENUM ('active', 'suspended', 'closed');

ALTER TABLE tenants
  ALTER COLUMN status DROP DEFAULT,
  ALTER COLUMN status TYPE tenant_status_legacy USING (
    CASE status::text
      WHEN 'active'        THEN 'active'::tenant_status_legacy
      WHEN 'suspended'     THEN 'suspended'::tenant_status_legacy
      WHEN 'archived'      THEN 'closed'::tenant_status_legacy
      ELSE                      'suspended'::tenant_status_legacy
    END
  ),
  ALTER COLUMN status SET DEFAULT 'active'::tenant_status_legacy;

DROP TYPE tenant_status;
ALTER TYPE tenant_status_legacy RENAME TO tenant_status;

ALTER TABLE tenants
  DROP COLUMN transactions_disabled,
  DROP COLUMN users_locked,
  DROP COLUMN operations_frozen;
