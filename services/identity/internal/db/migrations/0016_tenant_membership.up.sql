-- ═══════════════════════════════════════════════════════════════════
-- Membership settings (Phase 12/A) — registration-fee configuration
-- that the Tenant Super Admin sets once and the onboarding workflow
-- reads on every application.
--
-- Lives in its own table (mirrors tenant_branding / tenant_region /
-- tenant_operations) rather than getting bolted onto operations,
-- because the membership-onboarding subsystem will grow more fields
-- (welcome-letter template, default deposit product for new members,
-- member-number format, etc.) in later phases.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS tenant_membership (
  tenant_id                       uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,

  -- Master switch. When false, the onboarding workflow skips the
  -- registration-fee step entirely.
  collect_registration_fee        boolean       NOT NULL DEFAULT false,

  -- Per-applicant-type fee. May differ; both must be non-negative.
  registration_fee_individual     numeric(18,2) NOT NULL DEFAULT 0 CHECK (registration_fee_individual >= 0),
  registration_fee_institutional  numeric(18,2) NOT NULL DEFAULT 0 CHECK (registration_fee_institutional >= 0),

  -- Channels accepted as proof of payment. Stored as a text array so
  -- the UI can toggle them independently. Values mirror the deposit
  -- channel enum: 'mpesa' | 'airtel_money' | 'bank_transfer' |
  -- 'cash' | 'cheque'. Empty array = no channels enabled (an admin
  -- mistake, but allowed so the toggle can stay on while the SACCO
  -- decides which channels to support).
  accepted_payment_channels       text[]        NOT NULL DEFAULT ARRAY['mpesa','bank_transfer','cash']::text[],

  -- Governs the refund handling when a paid-up application is later
  -- declined. When true, the activation pipeline prompts an officer
  -- to post the refund leg; when false, the fee stays as SACCO income.
  fee_refundable_on_rejection     boolean       NOT NULL DEFAULT true,

  updated_at                      timestamptz   NOT NULL DEFAULT now()
);

CREATE TRIGGER tenant_membership_updated_at BEFORE UPDATE ON tenant_membership
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE tenant_membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_membership FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_tenant_membership ON tenant_membership
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant_membership TO nexus_app;

-- Seed a default row for every existing tenant so the GET endpoint
-- always returns a populated object.
INSERT INTO tenant_membership (tenant_id)
SELECT id FROM tenants
  ON CONFLICT (tenant_id) DO NOTHING;
