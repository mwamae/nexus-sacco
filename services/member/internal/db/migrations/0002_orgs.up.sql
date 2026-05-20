-- Organisation onboarding — the non-individual track.
--
-- A SACCO accepts both individuals (`members`) and organisations
-- (groups, chamas, limited companies, sole proprietorships, NGOs,
-- churches, sister SACCOs, cooperatives, schools). The two are
-- intentionally separate tables: their KYC requirements, document
-- mixes, and signatory rules look nothing alike, and pushing them
-- through one schema would mean a wall of nullable columns.

CREATE TYPE org_kind AS ENUM (
  'group', 'chama', 'ltd', 'sole_prop', 'ngo', 'church',
  'sacco', 'cooperative', 'school'
);

CREATE TYPE org_status AS ENUM (
  'pending', 'active', 'suspended', 'closed', 'rejected', 'dormant'
);

CREATE TYPE risk_category AS ENUM ('low', 'medium', 'high');

CREATE TYPE kyc_review_status AS ENUM (
  'not_started', 'in_review', 'verified', 'rejected'
);

CREATE TYPE signatory_class AS ENUM ('mandatory', 'optional', 'alternate');

CREATE TYPE org_doc_kind AS ENUM (
  'registration_certificate',
  'cr12',
  'kra_pin_certificate',
  'memorandum_articles',
  'constitution_bylaws',
  'business_permit',
  'tax_compliance_certificate',
  'vat_certificate',
  'ngo_certificate',
  'cooperative_certificate',
  'proof_of_address',
  'audited_financials',
  'bank_statement',
  'board_resolution',
  'signatory_appointment_resolution',
  'beneficial_ownership_declaration'
);

CREATE TYPE doc_verification AS ENUM ('pending', 'verified', 'rejected');

CREATE TYPE org_contact_kind AS ENUM ('primary', 'finance', 'hr_payroll', 'compliance');

CREATE TYPE official_position AS ENUM (
  'chairperson', 'vice_chairperson', 'treasurer', 'secretary',
  'director', 'trustee', 'principal', 'pastor', 'other'
);

-- ═══════════════════════════════════════════════════════════════════
-- ORG_MEMBERS — core profile.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_members (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  org_no               text NOT NULL,                    -- "ORG-YYYY-NNNNN" within tenant
  status               org_status NOT NULL DEFAULT 'pending',

  -- Identity
  registered_name      text NOT NULL,
  trading_name         text,
  kind                 org_kind NOT NULL,
  registration_no      text,
  date_of_registration date,
  date_of_operation    date,
  industry             text,
  nature_of_business   text,
  member_count         int,        -- members of the org (not SACCO members)
  employee_count       int,

  -- Location
  physical_address     text,
  postal_address       text,
  county               text,
  sub_county           text,
  ward                 text,
  gps_lat              numeric(9,6),
  gps_lng              numeric(9,6),
  branch_id            uuid REFERENCES tenant_branches(id) ON DELETE SET NULL,

  -- Compliance / approval
  risk_category        risk_category NOT NULL DEFAULT 'medium',
  kyc_status           kyc_review_status NOT NULL DEFAULT 'not_started',
  blacklisted          boolean NOT NULL DEFAULT false,
  blacklist_reason     text,
  dormant_since        date,                             -- nullable; presence means dormant

  approved_at          timestamptz,
  approved_by          uuid,
  rejection_reason     text,

  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,
  UNIQUE (tenant_id, org_no)
);
CREATE INDEX org_members_tenant_status_idx ON org_members (tenant_id, status);
CREATE INDEX org_members_tenant_name_idx   ON org_members (tenant_id, registered_name);
CREATE INDEX org_members_branch_idx        ON org_members (branch_id);

CREATE TRIGGER org_members_updated_at BEFORE UPDATE ON org_members
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Per-tenant year-keyed counter so org_no looks like ORG-2026-00001.
CREATE TABLE org_number_seq (
  tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  year       int  NOT NULL,
  last_value int  NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, year)
);

-- ═══════════════════════════════════════════════════════════════════
-- ORG_DOCUMENTS — one row per (org, kind) carrying file metadata,
-- expiry, and verification state.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_documents (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind            org_doc_kind NOT NULL,
  storage_path    text NOT NULL,
  mime            text NOT NULL,
  size_bytes      bigint NOT NULL,
  issue_date      date,
  expiry_date     date,
  verification    doc_verification NOT NULL DEFAULT 'pending',
  verified_by     uuid,
  verified_at     timestamptz,
  verification_note text,
  uploaded_at     timestamptz NOT NULL DEFAULT now(),
  uploaded_by     uuid,
  UNIQUE (org_id, kind)
);
CREATE INDEX org_documents_org_idx          ON org_documents (org_id);
CREATE INDEX org_documents_expiry_idx       ON org_documents (expiry_date) WHERE expiry_date IS NOT NULL;

-- ═══════════════════════════════════════════════════════════════════
-- ORG_OFFICIALS — directors, signatories, office-bearers.
-- Personal KYC fields mirror what we capture for individual members.
-- File uploads (passport photo, signature, ID copy, KRA PIN cert) live
-- in storage with paths derived by convention; we keep their metadata
-- in a small JSONB blob so we don't blow up the row width with 4
-- columns per file × 4 files.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_officials (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id               uuid NOT NULL REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

  -- Personal identity
  full_name            text NOT NULL,
  id_doc_kind          id_doc_kind NOT NULL DEFAULT 'national_id',
  id_doc_number        text NOT NULL,
  kra_pin              text,
  date_of_birth        date,
  gender               gender NOT NULL DEFAULT 'undisclosed',
  nationality          text,
  phone                text,
  email                citext,
  physical_address     text,
  occupation           text,

  -- Role in the org
  position             official_position NOT NULL DEFAULT 'director',
  position_label       text,                              -- free-form when position='other'
  appointed_on         date,

  -- Compliance flags
  is_pep               boolean NOT NULL DEFAULT false,
  pep_note             text,
  sanctions_screened_at timestamptz,
  sanctions_screened_by uuid,
  sanctions_hit        boolean NOT NULL DEFAULT false,
  sanctions_note       text,

  -- Beneficial ownership
  is_beneficial_owner  boolean NOT NULL DEFAULT false,
  ownership_percent    numeric(5,2),

  -- File metadata (passport_photo, signature, id_copy, kra_pin)
  -- Stored as JSON: { "passport_photo": {"mime":"image/jpeg","size":123,"updated_at":"..."}, ... }
  files                jsonb NOT NULL DEFAULT '{}'::jsonb,

  position_order       int NOT NULL DEFAULT 0,            -- ordering within the org
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX org_officials_org_idx ON org_officials (org_id, position_order);
CREATE TRIGGER org_officials_updated_at BEFORE UPDATE ON org_officials
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ═══════════════════════════════════════════════════════════════════
-- ORG_SIGNATORIES — links officials to signing rights.
-- mandate_rules is a JSON object with the rule set; we don't enforce
-- here — the loans / transactions services will consult it.
--   Example:
--     {
--       "default": "any_two",
--       "rules": [
--         { "above_amount": 500000, "require": ["chair", "treasurer"] }
--       ]
--     }
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_signatories (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  official_id     uuid NOT NULL REFERENCES org_officials(id) ON DELETE CASCADE,
  class           signatory_class NOT NULL DEFAULT 'mandatory',
  signing_order   int NOT NULL DEFAULT 0,
  txn_limit       numeric(18,2),                          -- nullable = no individual cap
  effective_from  date NOT NULL DEFAULT CURRENT_DATE,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, official_id)
);
CREATE INDEX org_signatories_org_idx ON org_signatories (org_id, signing_order);

-- A separate row holds the org-wide mandate-rule JSON so we don't have
-- to denormalise it per signatory.
CREATE TABLE org_mandate (
  org_id         uuid PRIMARY KEY REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  rules          jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER org_mandate_updated_at BEFORE UPDATE ON org_mandate
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ═══════════════════════════════════════════════════════════════════
-- ORG_BANKING — 1:1 banking + mobile-money + disbursement prefs.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_banking (
  org_id                  uuid PRIMARY KEY REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,

  bank_name               text,
  bank_branch             text,
  bank_code               text,
  swift_code              text,
  account_name            text,
  account_number          text,

  paybill                 text,
  till_number             text,
  mobile_money_phones     text,                           -- comma-separated for v1
  mobile_settlement_account text,                         -- "till"/"bank"/account#

  preferred_disbursement  text,                           -- "bank" | "mobile" | "cheque"
  preferred_repayment     text,                           -- "standing_order" | "checkoff" | "mpesa"
  standing_order_details  text,
  checkoff_arrangement    text,

  updated_at              timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER org_banking_updated_at BEFORE UPDATE ON org_banking
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ═══════════════════════════════════════════════════════════════════
-- ORG_CONTACTS — operational contacts (primary, finance, HR, compliance).
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE org_contacts (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES org_members(id) ON DELETE CASCADE,
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind        org_contact_kind NOT NULL,
  full_name   text NOT NULL,
  role        text,
  phone       text,
  email       citext,
  position    int NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, kind)                                   -- one per kind per org
);
CREATE INDEX org_contacts_org_idx ON org_contacts (org_id);

-- ═══════════════════════════════════════════════════════════════════
-- RLS — every table tenant-scoped via app.tenant_id.
-- ═══════════════════════════════════════════════════════════════════
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'org_members', 'org_number_seq', 'org_documents', 'org_officials',
    'org_signatories', 'org_mandate', 'org_banking', 'org_contacts'
  ])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON
  org_members, org_number_seq, org_documents, org_officials,
  org_signatories, org_mandate, org_banking, org_contacts
TO nexus_app;
