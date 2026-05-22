-- ═══════════════════════════════════════════════════════════════════
-- Unified Membership Application pipeline (Phase 12/B).
--
-- A single queue handles both individual and institutional onboarding.
-- An officer captures the application; a reviewer walks a checklist;
-- a configured approver (or workflow) finalises it. Once approved the
-- activation pipeline (Phase D) materialises the actual member row,
-- creates share + savings accounts, and posts the registration fee
-- to the GL.
--
-- Until activation lands, an approved application just flips status —
-- the existing /members/{id} and /orgs/{id} endpoints remain the
-- backend for already-created entities.
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── status enum ───────────
CREATE TYPE membership_application_status AS ENUM (
  'submitted',
  'under_review',
  'returned_for_correction',
  'reviewed_pending_approval',
  'approved_active',
  'declined',
  'withdrawn'
);

CREATE TYPE membership_application_kind AS ENUM ('individual', 'institutional');

-- ─────────── core ───────────
CREATE TABLE IF NOT EXISTS membership_applications (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_no           text NOT NULL,
  kind                     membership_application_kind NOT NULL,
  status                   membership_application_status NOT NULL DEFAULT 'submitted',

  -- Applicant identity (denormalised for the queue display + filters).
  -- The richer KYC payload lives in `applicant_payload` so we don't have
  -- to evolve the schema every time the form changes.
  applicant_name           text NOT NULL,            -- full name or registered name
  entity_type              text,                     -- institutional: chama, ltd, sole_prop, etc.; null for individuals
  primary_phone            text,
  primary_email            text,
  branch_id                uuid,                     -- optional branch association

  -- Free-form KYC + form state. Single JSON column avoids a schema
  -- migration per new form field.
  applicant_payload        jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- Registration-fee block. Populated when tenant_membership has the
  -- toggle on at submission time. fee_amount_due is snapshotted from
  -- the tenant setting at submit time so a later config change doesn't
  -- retroactively rewrite the application's fee.
  fee_required             boolean       NOT NULL DEFAULT false,
  fee_amount_due           numeric(18,2) NOT NULL DEFAULT 0,
  fee_amount_paid          numeric(18,2) NOT NULL DEFAULT 0,
  fee_payment_channel      text,
  fee_payment_reference    text,
  fee_payment_date         date,
  fee_proof_doc_path       text,
  fee_shortfall_note       text,
  fee_status               text NOT NULL DEFAULT 'not_required'
                            CHECK (fee_status IN ('not_required','paid','shortfall','not_paid','refund_pending','refunded')),

  -- Submission audit
  submitted_at             timestamptz NOT NULL DEFAULT now(),
  submitted_by             uuid NOT NULL,

  -- Review audit
  reviewer_user_id         uuid,
  review_started_at        timestamptz,
  review_completed_at      timestamptz,
  review_summary_note      text,

  -- Approval audit
  approver_user_id         uuid,
  approved_at              timestamptz,
  decline_reason           text,
  approval_conditions      text,
  workflow_instance_id     uuid,

  -- Lifecycle bookkeeping
  withdrawn_at             timestamptz,
  withdrawn_by             uuid,
  withdraw_reason          text,

  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now(),

  UNIQUE (tenant_id, application_no)
);

CREATE INDEX IF NOT EXISTS applications_tenant_status_idx
  ON membership_applications (tenant_id, status, submitted_at DESC);
CREATE INDEX IF NOT EXISTS applications_kind_idx
  ON membership_applications (tenant_id, kind);
CREATE INDEX IF NOT EXISTS applications_submitted_by_idx
  ON membership_applications (tenant_id, submitted_by, submitted_at DESC);

ALTER TABLE membership_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_applications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_membership_applications ON membership_applications
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_applications TO nexus_app;

-- ─────────── per-tenant checklist items ───────────
-- Each (kind, code) pair is unique. Items are configurable per tenant
-- but seeded with sensible defaults on first GET via the handler.
CREATE TABLE IF NOT EXISTS membership_application_checklist_items (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind          membership_application_kind NOT NULL,
  code          text NOT NULL,
  label         text NOT NULL,
  description   text,
  mandatory     boolean NOT NULL DEFAULT true,
  display_order int NOT NULL DEFAULT 0,
  is_active     boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, kind, code)
);
CREATE INDEX IF NOT EXISTS checklist_items_kind_idx
  ON membership_application_checklist_items (tenant_id, kind, display_order);

ALTER TABLE membership_application_checklist_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_application_checklist_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_checklist_items ON membership_application_checklist_items
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_application_checklist_items TO nexus_app;

-- ─────────── reviewer responses per application ───────────
CREATE TABLE IF NOT EXISTS membership_application_checklist_responses (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id    uuid NOT NULL REFERENCES membership_applications(id) ON DELETE CASCADE,
  checklist_code    text NOT NULL,                         -- denormalised key into checklist_items.code
  response          text NOT NULL CHECK (response IN ('confirmed','flagged','n/a')),
  note              text,
  responded_by      uuid NOT NULL,
  responded_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (application_id, checklist_code)
);
CREATE INDEX IF NOT EXISTS checklist_responses_app_idx
  ON membership_application_checklist_responses (application_id);

ALTER TABLE membership_application_checklist_responses ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_application_checklist_responses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_checklist_responses ON membership_application_checklist_responses
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_application_checklist_responses TO nexus_app;

-- ─────────── correction history ───────────
-- Each "return for correction" event records who returned the
-- application and what they asked the officer to fix. The officer's
-- response + re-submission lands as a fresh event.
CREATE TABLE IF NOT EXISTS membership_application_correction_history (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id   uuid NOT NULL REFERENCES membership_applications(id) ON DELETE CASCADE,
  event_kind       text NOT NULL CHECK (event_kind IN ('returned','resubmitted')),
  actor_user_id    uuid NOT NULL,
  note             text NOT NULL,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS correction_history_app_idx
  ON membership_application_correction_history (application_id, created_at DESC);

ALTER TABLE membership_application_correction_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_application_correction_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_correction_history ON membership_application_correction_history
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_application_correction_history TO nexus_app;

-- ─────────── documents ───────────
-- Single table for all uploaded files. `kind` is a free-form tag
-- (national_id, kra_pin, passport_photo, signature, registration_cert,
-- cr12, constitution, board_resolution, fee_proof, …) so the form can
-- evolve without schema churn.
CREATE TABLE IF NOT EXISTS membership_application_documents (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id   uuid NOT NULL REFERENCES membership_applications(id) ON DELETE CASCADE,
  kind             text NOT NULL,
  filename         text NOT NULL,
  mime_type        text NOT NULL,
  size_bytes       bigint NOT NULL,
  storage_path     text NOT NULL,
  uploaded_at      timestamptz NOT NULL DEFAULT now(),
  uploaded_by      uuid NOT NULL,
  UNIQUE (application_id, kind)
);
CREATE INDEX IF NOT EXISTS app_docs_app_idx
  ON membership_application_documents (application_id);

ALTER TABLE membership_application_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_application_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_app_documents ON membership_application_documents
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_application_documents TO nexus_app;

-- ─────────── application number sequence ───────────
-- Per-tenant counter. The handler formats as APP-YYYY-NNNNNN.
CREATE TABLE IF NOT EXISTS membership_application_seq (
  tenant_id    uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  year         int  NOT NULL,
  last_no      int  NOT NULL DEFAULT 0
);
ALTER TABLE membership_application_seq ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_application_seq FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_application_seq ON membership_application_seq
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON membership_application_seq TO nexus_app;

-- ─────────── default checklist seed ───────────
-- The reviewer screen needs sensible defaults out of the box. The
-- per-tenant config endpoint can override these later; existing rows
-- are preserved by ON CONFLICT.
INSERT INTO membership_application_checklist_items
  (tenant_id, kind, code, label, description, mandatory, display_order)
SELECT t.id, c.kind::membership_application_kind, c.code, c.label, c.descr, c.mandatory, c.ord
  FROM tenants t CROSS JOIN (VALUES
    -- INDIVIDUAL
    ('individual', 'meets_criteria',      'Applicant meets SACCO membership criteria',
       'Common bond, age, residency — per the SACCO''s by-laws.', true, 10),
    ('individual', 'id_valid',            'National ID / Passport is valid and matches application',
       'ID document is legible, in-date, and the name + number match the form.', true, 20),
    ('individual', 'kra_pin_uploaded',    'KRA PIN certificate uploaded and matches',
       'PIN on the certificate matches the form field.', true, 30),
    ('individual', 'photo_acceptable',    'Passport photo is clear and acceptable',
       'Recent, plain background, face fully visible.', true, 40),
    ('individual', 'signature_uploaded',  'Signature specimen uploaded',
       'On file for cheque + withdrawal authorisation.', true, 50),
    ('individual', 'nok_complete',        'Next of kin details complete',
       'Name, relationship, phone, ID number.', true, 60),
    ('individual', 'fee_paid',            'Registration fee paid in full with valid proof',
       'Channel reference present and matches the configured fee amount.', true, 70),
    ('individual', 'no_adverse_match',    'No adverse CRB or internal blacklist match',
       'Run the screening before confirming.', true, 80),

    -- INSTITUTIONAL
    ('institutional', 'entity_registered',     'Entity type and registration details verified',
       'Cross-checked with the registry of companies / NGOs / cooperative societies.', true, 10),
    ('institutional', 'registration_cert',     'Registration certificate uploaded and valid',
       'In-date and the registration number matches the form.', true, 20),
    ('institutional', 'cr12_uploaded',         'CR12 or equivalent uploaded (where applicable)',
       'Limited companies + sole proprietorships; skip for chamas/groups.', false, 30),
    ('institutional', 'kra_pin_uploaded',      'KRA PIN certificate uploaded',
       'Entity PIN, not director PINs.', true, 40),
    ('institutional', 'constitution_uploaded', 'Constitution / bylaws / M&A uploaded',
       'Governing document showing membership eligibility + decision-making.', true, 50),
    ('institutional', 'directors_kyc',         'All directors / officials have complete KYC',
       'Each named official has individual KYC on file.', true, 60),
    ('institutional', 'signatories_defined',   'Signatories and mandate rules defined',
       'Bank-style mandate: how many signatories per amount band, named individuals.', true, 70),
    ('institutional', 'board_resolution',      'Board resolution to join SACCO uploaded',
       'Dated and signed by the chair / secretary.', true, 80),
    ('institutional', 'beneficial_ownership',  'Beneficial ownership declaration completed',
       'Per Anti-Money Laundering requirements.', true, 90),
    ('institutional', 'fee_paid',              'Registration fee paid in full with valid proof',
       'Channel reference present; institutional fee usually higher than individual.', true, 100),
    ('institutional', 'no_adverse_match',      'No adverse matches on directors or entity',
       'Run screening against sanctions + internal blacklist.', true, 110)
  ) AS c(kind, code, label, descr, mandatory, ord)
 ON CONFLICT (tenant_id, kind, code) DO NOTHING;
