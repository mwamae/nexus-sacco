-- Loans Phase 2 — SASRA extract metadata.
--
-- The SASRA quarterly extract ships with a DRAFT watermark by
-- default. A tenant admin clears the watermark for a given period
-- by clicking "Mark column layout verified for {period}" in the UI,
-- which writes a row here. Subsequent quarters re-use the
-- verification until the SASRA form version changes.
--
-- This guarantees a SACCO compliance officer can't accidentally
-- treat the output as final without having compared it against the
-- current Form D. The verification record persists per-period so a
-- new quarter inherits the same sign-off (the form layout doesn't
-- typically change between quarters; when SASRA does change the form
-- the operator bumps verified_form_version on the new period).
--
-- tenant_sasra_column_overrides is the per-tenant escape hatch the
-- prompt spec'd: when SASRA changes the column layout between
-- releases, a tenant admin uploads the new layout JSON via Settings
-- → Loans policy → SASRA layout. The extract handler reads from
-- this table first; falls back to the embedded
-- services/savings/internal/handler/sasra_columns.json when no row
-- exists for the tenant.
--
-- Both tables are tenant-scoped via RLS.

CREATE TABLE IF NOT EXISTS sasra_extract_verifications (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  period                text NOT NULL,
  verified_by           uuid NOT NULL,
  verified_at           timestamptz NOT NULL DEFAULT now(),
  -- Free-text label for the SASRA form revision the user verified
  -- against ("Form D 2025-Q4", "Form NW-DTS v3.1", etc). Lets the
  -- next-quarter operator confirm the same revision still applies.
  verified_form_version text NOT NULL,
  -- Optional note the verifier can leave (e.g. "verified against
  -- PDF circulated 2026-03-15 via SASRA email").
  note                  text,
  UNIQUE (tenant_id, period)
);

CREATE INDEX IF NOT EXISTS sasra_extract_verifications_tenant_idx
  ON sasra_extract_verifications (tenant_id, period);

ALTER TABLE sasra_extract_verifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE sasra_extract_verifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_sasra_verifications ON sasra_extract_verifications
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON sasra_extract_verifications TO nexus_app;


CREATE TABLE IF NOT EXISTS tenant_sasra_column_overrides (
  tenant_id      uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  -- JSON shape mirrors services/savings/internal/handler/sasra_columns.json.
  -- The handler json-decodes this into the same struct.
  layout         jsonb NOT NULL,
  -- Provenance for audit — who uploaded the override + when.
  updated_by     uuid NOT NULL,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  form_version   text NOT NULL
);

ALTER TABLE tenant_sasra_column_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_sasra_column_overrides FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_sasra_overrides ON tenant_sasra_column_overrides
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_sasra_column_overrides TO nexus_app;
