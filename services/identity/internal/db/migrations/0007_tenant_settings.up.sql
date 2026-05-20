-- Tenant-side configuration.
-- Three 1:1 tables (one row per tenant) — split rather than one JSONB blob
-- so each area can be validated/queried independently and so RLS still
-- applies cleanly. All rows are tenant-scoped via app.tenant_id.

CREATE TYPE interest_method AS ENUM ('flat', 'reducing_balance', 'declining_balance');

-- ─────────── Branding & white-labeling ───────────
CREATE TABLE tenant_branding (
  tenant_id          uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  -- Logo: storage_path is opaque to the storage backend (LocalDisk for now).
  logo_storage_path  text,
  logo_mime          text,
  logo_size_bytes    bigint,
  logo_updated_at    timestamptz,
  -- Visual tokens. Stored as hex so the frontend can drop them straight
  -- onto CSS custom properties.
  primary_color      text NOT NULL DEFAULT '#1F8A5B',
  accent_color       text NOT NULL DEFAULT '#1F8A5B',
  font_family        text NOT NULL DEFAULT 'IBM Plex Sans',
  -- Communications-channel overrides.
  email_from_name    text,                       -- "Tujenge SACCO" instead of "nexusSacco"
  sms_sender_id      text,                       -- mobile shortcode / alphanumeric
  -- Custom subdomain / domain — string only for v1; DNS + cert provisioning is a separate workstream.
  custom_domain      text,
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER tenant_branding_updated_at BEFORE UPDATE ON tenant_branding
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── Region / locale / regulation ───────────
CREATE TABLE tenant_region (
  tenant_id            uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  timezone             text NOT NULL DEFAULT 'Africa/Nairobi',  -- IANA name
  language             text NOT NULL DEFAULT 'en',              -- ISO-639 2-letter
  date_format          text NOT NULL DEFAULT 'YYYY-MM-DD',
  regulator            text,                                    -- e.g. SASRA, CBK, BoT
  jurisdiction         text,                                    -- e.g. "Kenya", "Tanzania"
  vat_rate             numeric(5,2) NOT NULL DEFAULT 16.00,     -- percent
  withholding_tax_rate numeric(5,2) NOT NULL DEFAULT 0.00,
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER tenant_region_updated_at BEFORE UPDATE ON tenant_region
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── Operations (tenant-wide defaults) ───────────
CREATE TABLE tenant_operations (
  tenant_id                 uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,

  -- Lending defaults
  loan_min_amount           numeric(18,2) NOT NULL DEFAULT 1000,
  loan_max_amount           numeric(18,2) NOT NULL DEFAULT 1000000,
  loan_max_term_months      int          NOT NULL DEFAULT 24,
  default_interest_method   interest_method NOT NULL DEFAULT 'reducing_balance',
  default_interest_rate     numeric(6,3)  NOT NULL DEFAULT 12.000,  -- % per annum

  -- Savings rules
  savings_min_opening_bal   numeric(18,2) NOT NULL DEFAULT 100,
  savings_min_running_bal   numeric(18,2) NOT NULL DEFAULT 100,
  savings_withdrawal_fee    numeric(18,2) NOT NULL DEFAULT 0,

  -- Dividend rules
  dividend_rate             numeric(6,3) NOT NULL DEFAULT 8.000,    -- % per annum
  dividend_frequency        text         NOT NULL DEFAULT 'annual', -- annual | semi_annual | quarterly

  -- Penalty rules
  penalty_late_fee_rate     numeric(6,3) NOT NULL DEFAULT 1.000,    -- % of overdue per period
  penalty_grace_period_days int          NOT NULL DEFAULT 7,

  -- Guarantor policies
  guarantor_min_count       int          NOT NULL DEFAULT 2,
  guarantor_self_max_amount numeric(18,2) NOT NULL DEFAULT 50000,

  -- Approval thresholds (in tenant currency)
  approval_branch_limit     numeric(18,2) NOT NULL DEFAULT 100000,
  approval_credit_limit     numeric(18,2) NOT NULL DEFAULT 500000,
  approval_board_limit      numeric(18,2) NOT NULL DEFAULT 2000000,

  updated_at                timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER tenant_operations_updated_at BEFORE UPDATE ON tenant_operations
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── RLS ───────────
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['tenant_branding', 'tenant_region', 'tenant_operations'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON
  tenant_branding, tenant_region, tenant_operations
TO nexus_app;
