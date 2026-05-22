-- Re-adds the column as not-null default false. Won't restore the
-- flag-on tenants — operators would need to flip the flag manually.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS unified_counterparties boolean NOT NULL DEFAULT false;
