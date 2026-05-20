-- Member service initial schema. Lives in the same database as identity
-- so we can FK to tenants and share the nexus_app application role.

-- ═══════════════════════════════════════════════════════════════════
-- MEMBERS — individual SACCO members. Tenant-scoped.
-- A member is not a user (no login). If they ever need a portal login
-- we'll link to identity.users via an optional user_id column later.
-- ═══════════════════════════════════════════════════════════════════
CREATE TYPE member_status AS ENUM ('pending', 'active', 'suspended', 'closed', 'rejected');
CREATE TYPE id_doc_kind AS ENUM ('national_id', 'passport', 'alien_id');
CREATE TYPE gender AS ENUM ('male', 'female', 'other', 'undisclosed');

CREATE TABLE members (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_no            text NOT NULL,                 -- e.g. M-2026-00001, unique within tenant
  status               member_status NOT NULL DEFAULT 'pending',

  -- Identity
  full_name            text NOT NULL,
  id_doc_kind          id_doc_kind NOT NULL DEFAULT 'national_id',
  id_doc_number        text NOT NULL,                 -- National ID or passport #
  kra_pin              text,                          -- Kenyan tax PIN (optional in v1)
  gender               gender NOT NULL DEFAULT 'undisclosed',
  date_of_birth        date,

  -- Contact
  phone                text,
  email                citext,

  -- Address
  county               text,
  sub_county           text,
  physical_address     text,

  -- Employment
  employment_status    text,                          -- "employed", "self-employed", "unemployed", "retired", "student"
  employer             text,
  payroll_no           text,
  job_title            text,

  -- Approval audit
  approved_at          timestamptz,
  approved_by          uuid,                          -- references identity.users(id) — not FKd cross-service
  rejection_reason     text,

  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  created_by           uuid,                          -- identity user that onboarded them
  UNIQUE (tenant_id, member_no),
  UNIQUE (tenant_id, id_doc_kind, id_doc_number)
);
CREATE INDEX members_tenant_status_idx ON members (tenant_id, status);
CREATE INDEX members_tenant_name_idx ON members (tenant_id, full_name);

CREATE TRIGGER members_updated_at BEFORE UPDATE ON members
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ═══════════════════════════════════════════════════════════════════
-- MEMBER RELATIONS — next of kin + beneficiaries collapsed into one
-- table differentiated by `kind`. Beneficiaries also carry a share %.
-- ═══════════════════════════════════════════════════════════════════
CREATE TYPE relation_kind AS ENUM ('next_of_kin', 'beneficiary');

CREATE TABLE member_relations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  member_id       uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind            relation_kind NOT NULL,
  full_name       text NOT NULL,
  relationship    text NOT NULL,                     -- "spouse", "parent", "sibling", "child", ...
  phone           text,
  email           citext,
  id_doc_number   text,                              -- optional national ID
  share_percent   numeric(5,2),                      -- only for beneficiaries; sum should be 100 across a member's beneficiaries
  position        int NOT NULL DEFAULT 0,            -- ordering within (member, kind)
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX member_relations_member_idx ON member_relations (member_id, kind, position);

-- ═══════════════════════════════════════════════════════════════════
-- MEMBER DOCUMENTS — small set of well-known kinds for v1
-- (signature + passport_photo). Storage path is opaque; the storage
-- driver resolves it. mime + size let us serve back correctly.
-- ═══════════════════════════════════════════════════════════════════
CREATE TYPE document_kind AS ENUM ('signature', 'passport_photo', 'id_front', 'id_back');

CREATE TABLE member_documents (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  member_id       uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind            document_kind NOT NULL,
  storage_path    text NOT NULL,                     -- opaque key in the storage backend
  mime            text NOT NULL,
  size_bytes      bigint NOT NULL,
  uploaded_at     timestamptz NOT NULL DEFAULT now(),
  uploaded_by     uuid,
  UNIQUE (member_id, kind)                            -- one current of each kind per member
);
CREATE INDEX member_documents_member_idx ON member_documents (member_id);

-- ═══════════════════════════════════════════════════════════════════
-- MEMBER NUMBER SEQUENCE — per-tenant, per-year counter. Used to
-- generate "M-YYYY-NNNNN" identifiers in MemberStore.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE member_number_seq (
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  year            int  NOT NULL,
  last_value      int  NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, year)
);

-- ═══════════════════════════════════════════════════════════════════
-- RLS — same pattern as identity: enforce tenant via app.tenant_id GUC.
-- ═══════════════════════════════════════════════════════════════════
DO $$
DECLARE
  t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['members', 'member_relations', 'member_documents', 'member_number_seq'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;

CREATE POLICY tenant_isolation_members ON members
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_member_relations ON member_relations
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_member_documents ON member_documents
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_member_number_seq ON member_number_seq
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- Grants on the application role (created by identity migration 0001).
GRANT SELECT, INSERT, UPDATE, DELETE ON
  members, member_relations, member_documents, member_number_seq
TO nexus_app;
