-- Loans Phase 6 — CRB + insurance + member self-service flag.
--
-- This migration is the foundation. Three concerns:
--
--   1. crb_credentials + crb_pulls — for the CRB integration. The
--      credentials column stores envelope-encrypted JSON (matching the
--      services/mpesa/internal/crypto/envelope.go pattern). The pulls
--      table records every CRB query: consent flag + signature path +
--      raw response + normalised score/rating/listings. A loan
--      application can reference its pull(s) via application_id.
--
--   2. insurance_providers + loan_insurance_policies — per-tenant
--      provider config (Britam / APA / Jubilee / CIC / custom) with
--      premium rate + min/max caps. Each loan disbursed against a
--      product flagged insurance_mandatory writes one policy row.
--
--   3. loan_products extension — insurance_provider_id +
--      insurance_mandatory.
--
-- Member self-service flag lives on members. Identity service handles
-- the member-login table separately (see identity/0037).

-- ─────────── ENUMs ───────────

CREATE TYPE crb_provider AS ENUM ('metropol','transunion','crb_africa');

-- ─────────── crb_credentials ───────────

CREATE TABLE IF NOT EXISTS crb_credentials (
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider       crb_provider NOT NULL,
  ciphertext     bytea NOT NULL,
  key_id         text NOT NULL,
  base_url       text,                                   -- override default endpoint (sandbox vs prod)
  active         boolean NOT NULL DEFAULT true,
  effective_from timestamptz NOT NULL DEFAULT now(),
  created_by     uuid NOT NULL,
  PRIMARY KEY (tenant_id, provider, effective_from)
);
ALTER TABLE crb_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE crb_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_crb_credentials ON crb_credentials
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON crb_credentials TO nexus_app;

-- ─────────── crb_pulls ───────────

CREATE TABLE IF NOT EXISTS crb_pulls (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider               crb_provider NOT NULL,
  member_id              uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  application_id         uuid REFERENCES loan_applications(id) ON DELETE SET NULL,
  pulled_at              timestamptz NOT NULL DEFAULT now(),
  pulled_by              uuid NOT NULL,
  consent_recorded       boolean NOT NULL DEFAULT false,
  consent_signature_path text,
  request_payload        jsonb,
  response_payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  score                  int,
  rating                 text,
  listings_count         int NOT NULL DEFAULT 0,
  enquiries_count        int NOT NULL DEFAULT 0,
  total_active_credit    numeric(18,2) NOT NULL DEFAULT 0,
  outstanding_balance    numeric(18,2) NOT NULL DEFAULT 0,
  status                 text NOT NULL DEFAULT 'success'
    CHECK (status IN ('success','failed','timeout','no_record')),
  error_message          text,
  sandbox                boolean NOT NULL DEFAULT false   -- true when pull came from stub adapter
);
CREATE INDEX IF NOT EXISTS crb_pulls_member_idx ON crb_pulls (member_id, pulled_at DESC);
CREATE INDEX IF NOT EXISTS crb_pulls_application_idx ON crb_pulls (application_id) WHERE application_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS crb_pulls_tenant_provider_idx ON crb_pulls (tenant_id, provider, pulled_at DESC);
ALTER TABLE crb_pulls ENABLE ROW LEVEL SECURITY;
ALTER TABLE crb_pulls FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_crb_pulls ON crb_pulls
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON crb_pulls TO nexus_app;

-- ─────────── insurance_providers ───────────

CREATE TABLE IF NOT EXISTS insurance_providers (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name                   text NOT NULL,
  provider_code          text NOT NULL,                  -- britam | apa | jubilee | cic | custom
  product_code           text NOT NULL,                  -- vendor's internal product code
  is_active              boolean NOT NULL DEFAULT true,
  credentials_ciphertext bytea,
  credentials_key_id     text,
  base_url               text,
  premium_rate_pct       numeric(7,4) NOT NULL,          -- e.g. 0.5000 for 0.5%
  min_premium            numeric(18,2) NOT NULL DEFAULT 0,
  max_premium            numeric(18,2),
  coverage_terms         text,
  sandbox                boolean NOT NULL DEFAULT true,  -- default to sandbox/stub until real creds wired
  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, provider_code, product_code)
);
CREATE INDEX IF NOT EXISTS insurance_providers_tenant_idx
  ON insurance_providers (tenant_id, is_active);
ALTER TABLE insurance_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE insurance_providers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_insurance_providers ON insurance_providers
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON insurance_providers TO nexus_app;

-- ─────────── loan_products extensions ───────────

ALTER TABLE loan_products
  ADD COLUMN IF NOT EXISTS insurance_provider_id uuid REFERENCES insurance_providers(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS insurance_mandatory   boolean NOT NULL DEFAULT false;

-- ─────────── loan_insurance_policies ───────────

CREATE TABLE IF NOT EXISTS loan_insurance_policies (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id             uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  provider_id         uuid NOT NULL REFERENCES insurance_providers(id) ON DELETE RESTRICT,
  policy_no           text,                              -- assigned by provider; null for sandbox until acknowledged
  premium_amount      numeric(18,2) NOT NULL,
  coverage_amount     numeric(18,2) NOT NULL,
  effective_from      date NOT NULL,
  effective_to        date NOT NULL,
  status              text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','active','expired','cancelled','claim_in_progress','paid')),
  vendor_response     jsonb,
  claim_record_id     uuid,
  sandbox             boolean NOT NULL DEFAULT false,
  created_at          timestamptz NOT NULL DEFAULT now(),
  cancelled_at        timestamptz,
  cancellation_reason text,
  UNIQUE (loan_id)                                       -- one policy per loan (idempotent on retry)
);
CREATE INDEX IF NOT EXISTS loan_insurance_provider_idx ON loan_insurance_policies (provider_id);
CREATE INDEX IF NOT EXISTS loan_insurance_status_idx ON loan_insurance_policies (tenant_id, status);
ALTER TABLE loan_insurance_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_insurance_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_insurance_policies ON loan_insurance_policies
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_insurance_policies TO nexus_app;

-- ─────────── members.has_self_service ───────────
--
-- Tenant opt-in for the member portal. Defaults to false so existing
-- members are not auto-exposed to the new flow. SACCOs flip this per
-- member as they activate self-service.

ALTER TABLE members
  ADD COLUMN IF NOT EXISTS has_self_service boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN members.has_self_service IS
  'Phase 6 — true when this member is enrolled in the member self-service portal. Flip via /v1/members/{id}/enable-self-service (admin-only).';

-- ─────────── tenant default insurance provider seed ───────────
--
-- Seed a per-tenant custom/stub insurance provider so dev tenants can
-- exercise the disbursement-with-insurance path immediately. Real
-- tenants edit this row in Settings → Insurance providers.

INSERT INTO insurance_providers (tenant_id, name, provider_code, product_code, premium_rate_pct, min_premium, sandbox)
SELECT t.id, 'Sandbox Insurer', 'custom', 'credit_life_default', 0.5000, 100.00, true
  FROM tenants t WHERE t.slug <> 'platform'
ON CONFLICT (tenant_id, provider_code, product_code) DO NOTHING;
