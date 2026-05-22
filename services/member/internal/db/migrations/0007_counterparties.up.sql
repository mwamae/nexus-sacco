-- Unified counterparty register — Phase A of the
-- members + org_members merger.
--
-- See docs/unified-counterparty-merge.md (added in this PR) for the
-- full design. Two-phase, behind feature flag
-- tenant_operations.unified_counterparties:
--   * Phase A (this migration) — additive. Creates the new table,
--     adds bridge counterparty_id columns on the existing tables,
--     adds the per-tenant CP-YYYY-NNNNN sequence. ZERO behaviour
--     change with the flag off. Migration 0008 does the backfill.
--   * Phase B (flag-on per tenant) — read paths switch over;
--     mirror writes continue. Done in Go code, not SQL.
--   * Phase C (separate PR) — drop mirror writes + legacy FKs.

-- ─────────── ENUMs ───────────

-- counterparty_kind — the 7 kinds the spec calls for. Individual is
-- the catch-all natural person; the other 6 are organisation
-- subtypes that map onto the legacy org_kind enum (group + chama
-- collapse to chama; ltd + sole_prop + cooperative collapse to
-- company; sacco kind drops to other for now).
CREATE TYPE counterparty_kind AS ENUM (
  'individual',
  'chama',
  'company',
  'ngo',
  'church',
  'school',
  'other'
);

-- counterparty_status — superset of member_status + org_status so a
-- single status column covers both shapes.
CREATE TYPE counterparty_status AS ENUM (
  'pending',
  'active',
  'dormant',
  'suspended',
  'blacklisted',
  'exited',
  'deceased',
  'rejected'
);

-- kyc_state — mirrors kyc_review_status from the org_members
-- migration; renamed slightly so callers can grep distinctly.
CREATE TYPE counterparty_kyc_state AS ENUM (
  'not_started',
  'in_review',
  'verified',
  'rejected'
);

CREATE TYPE counterparty_risk_band AS ENUM ('low', 'medium', 'high', 'n_a');

-- ─────────── counterparties ───────────

CREATE TABLE counterparties (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  cp_number       text NOT NULL,                       -- CP-YYYY-NNNNN per tenant
  legacy_id       text,                                -- preserved M-* or ORG-* if any
  kind            counterparty_kind NOT NULL,
  display_name    text NOT NULL,
  trading_as      text,                                -- usually only orgs
  status          counterparty_status NOT NULL DEFAULT 'pending',
  kyc_state       counterparty_kyc_state NOT NULL DEFAULT 'not_started',
  risk_band       counterparty_risk_band NOT NULL DEFAULT 'n_a',
  registration_no text,                                -- only orgs

  -- Kind-specific bags. The CHECK constraints below pin exactly one
  -- of these populated, never both.
  individual  jsonb,                                   -- {gender, dob, id_type, id_number, kra_pin,
                                                       --   marital_status, occupation, employer,
                                                       --   monthly_income, next_of_kin{...}}
  institution jsonb,                                   -- {registration_no, registered_date,
                                                       --   constitution_url, officials:[],
                                                       --   signatories:[], beneficial_owners:[],
                                                       --   board_resolutions:[]}

  contact     jsonb NOT NULL DEFAULT '{}'::jsonb,      -- {phone, email, county, sub_county,
                                                       --   ward, physical_address, postal_address}

  joined_at   timestamptz NOT NULL DEFAULT now(),
  closed_at   timestamptz,

  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  created_by  uuid,
  updated_by  uuid,

  UNIQUE (tenant_id, cp_number),

  -- Kind-payload pairing — the headline contract of the unified shape.
  CONSTRAINT cp_kind_payload_individual CHECK (
    kind <> 'individual' OR (individual IS NOT NULL AND institution IS NULL)
  ),
  CONSTRAINT cp_kind_payload_institution CHECK (
    kind = 'individual' OR (institution IS NOT NULL AND individual IS NULL)
  )
);

CREATE INDEX counterparties_tenant_idx     ON counterparties (tenant_id, status, kind);
CREATE INDEX counterparties_legacy_id_idx  ON counterparties (tenant_id, legacy_id) WHERE legacy_id IS NOT NULL;
CREATE INDEX counterparties_display_name_idx ON counterparties (tenant_id, display_name);
-- Used by the unified search across cp_number / legacy_id / display_name /
-- contact.phone / contact.email — search performance work lands in a follow-up.

-- ─────────── Bridge columns on the legacy tables ───────────
--
-- Every existing members / org_members row will get a counterparties
-- row + this FK populated in the backfill migration. UNIQUE because
-- the relationship is 1:1: a counterparty is either the individual
-- side of a person OR the organisation side of an entity, never both.

ALTER TABLE members
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX members_counterparty_idx
  ON members (counterparty_id) WHERE counterparty_id IS NOT NULL;

ALTER TABLE org_members
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX org_members_counterparty_idx
  ON org_members (counterparty_id) WHERE counterparty_id IS NOT NULL;

-- ─────────── RLS ───────────
--
-- Same tenant-isolation policy every other tenant-scoped table in
-- this DB uses. current_tenant_id() returns the session GUC set by
-- WithTenantTx.

ALTER TABLE counterparties ENABLE ROW LEVEL SECURITY;
ALTER TABLE counterparties FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_counterparties ON counterparties
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- ─────────── Sequence reuse note ───────────
--
-- cp_number generation piggybacks on the existing share_number_seq
-- table (the table-name predates its multi-domain use; kind='counterparty'
-- discriminates). No new sequence table needed. The Go helper that
-- mints CP-YYYY-NNNNN lives at services/member/internal/store/counterparty_store.go
-- and calls nextSeq("counterparty", "CP") with the same shared
-- implementation savings already uses for LA-/L-/SHA-/etc.

COMMENT ON TABLE counterparties IS
  'Unified register of natural persons + organisations. Phase A of the members/org_members merger. See migration 0007 + docs/unified-counterparty-merge.md.';
