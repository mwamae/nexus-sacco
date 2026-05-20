-- Expand tenant statuses + add operational restriction toggles.
--
-- Postgres can't drop or rename enum values cleanly, so we swap the
-- column to a fresh type with the new value set, mapping the old
-- `closed` rows onto `archived`.

CREATE TYPE tenant_status_v2 AS ENUM (
  'active', 'trial', 'suspended', 'expired', 'pending_setup', 'archived'
);

ALTER TABLE tenants
  ALTER COLUMN status DROP DEFAULT,
  ALTER COLUMN status TYPE tenant_status_v2 USING (
    CASE status::text
      WHEN 'closed' THEN 'archived'::tenant_status_v2
      ELSE status::text::tenant_status_v2
    END
  ),
  ALTER COLUMN status SET DEFAULT 'pending_setup'::tenant_status_v2;

DROP TYPE tenant_status;
ALTER TYPE tenant_status_v2 RENAME TO tenant_status;

-- Operational restriction toggles. These are independent of the
-- lifecycle status: an `active` tenant can still have any combination
-- of these flipped on (e.g. transactions disabled during a regulator
-- investigation while the rest of the system keeps running).
ALTER TABLE tenants
  ADD COLUMN operations_frozen     boolean NOT NULL DEFAULT false,
  ADD COLUMN users_locked          boolean NOT NULL DEFAULT false,
  ADD COLUMN transactions_disabled boolean NOT NULL DEFAULT false;
