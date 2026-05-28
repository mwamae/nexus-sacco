-- Loans Phase 4 — Collections workflow extensions.
--
-- IMPORTANT: this migration EXTENDS the existing Phase 6e collections
-- subsystem (migration 0007), it does not replace it. The pre-existing
-- tables (loan_collection_cases, loan_collection_contacts,
-- loan_promises_to_pay, loan_legal_cases) and the LoanCollectionsStore
-- + /v1/collection-cases/* routes that depend on them remain
-- authoritative for the "case + contacts + PTP" model.
--
-- Phase 4 layers on top:
--
--   • New loan_collection_events  — workflow events (PTP lifecycle,
--                                    escalation, legal handover, note,
--                                    auto-sms, letter generated, etc.)
--                                    that don't fit the contact model.
--                                    The Phase 4 timeline UNIONs the
--                                    existing contacts with this table.
--
--   • New loan_assignment_history  — append-only history of officer
--                                     (re)assignments per case. The
--                                     CURRENT assignment continues to
--                                     live on loan_collection_cases
--                                     (assigned_to / assigned_at).
--
--   • cancel_reason column on the legacy loan_promises_to_pay (already
--     supports 'cancelled' status; was missing the reason field).
--
--   • New collections_escalation_rules — per-tenant DPD → role/letter
--                                         ladder driving the
--                                         collections-engine worker.
--
--   • New collections_message_templates — per-tenant SMS/email body
--                                          templates keyed by DPD min.
--
--   • New dividend_offset_postings — audit + dedup for the dividend
--                                     offset policy (design decision 9.3).
--
--   • New tenant_operations.dividend_offset_policy column.
--
--   • loan_doc_kind enum extended with the four letter kinds.
--
-- The migration runner wraps each file in a tx; no explicit
-- BEGIN/COMMIT. ALTER TYPE ADD VALUE works in PG 12+ inside a tx
-- provided the new value isn't used in the same tx — we don't.

-- ─────────── ENUMs ───────────

-- Workflow event kinds for the new loan_collection_events table.
-- DELIBERATELY distinct from loan_contact_kind: that's the channel of
-- a human-logged contact (call/sms/whatsapp/email/in_person_visit/letter).
-- This is the *workflow* event (system-fired or operator action).
CREATE TYPE loan_collection_event_kind AS ENUM (
  'note',             -- free-form officer note (also fired by Phase 4 /notes endpoint)
  'auto_sms',         -- system-fired SMS at a DPD threshold
  'auto_email',       -- system-fired email
  'ptp_created',
  'ptp_kept',
  'ptp_broken',
  'ptp_cancelled',
  'escalation',
  'legal_handover',
  'assigned',
  'unassigned',
  'letter_generated'  -- collections-engine generated a letter PDF
);

-- Letter kinds for the auto-generated PDFs. Used in
-- collections_escalation_rules + loan_collection_events.details.
CREATE TYPE collections_letter_kind AS ENUM (
  'pre_collection',   -- DPD ~7
  'demand',           -- DPD ~30
  'final_demand',     -- DPD ~60
  'legal_notice'      -- DPD 90+
);

-- ─────────── loan_collection_events ───────────

CREATE TABLE loan_collection_events (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_id       uuid REFERENCES loan_collection_cases(id) ON DELETE SET NULL,
  loan_id       uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  kind          loan_collection_event_kind NOT NULL,
  occurred_at   timestamptz NOT NULL DEFAULT now(),
  created_by    uuid,                                       -- nullable: system events have no creator
  details       jsonb NOT NULL DEFAULT '{}'::jsonb,
  letter_kind   collections_letter_kind,                    -- denormalised filter
  amount        numeric(18,2),                              -- for ptp_created/kept/broken
  promised_date date                                        -- for ptp_created
);

CREATE INDEX loan_collection_events_loan_idx
  ON loan_collection_events (loan_id, occurred_at DESC);
CREATE INDEX loan_collection_events_case_idx
  ON loan_collection_events (case_id, occurred_at DESC) WHERE case_id IS NOT NULL;
CREATE INDEX loan_collection_events_tenant_kind_idx
  ON loan_collection_events (tenant_id, kind, occurred_at DESC);

ALTER TABLE loan_collection_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_collection_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_collection_events ON loan_collection_events
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_collection_events TO nexus_app;

-- ─────────── loan_assignment_history ───────────
--
-- Append-only history. The CURRENT assignment lives on
-- loan_collection_cases (assigned_to + assigned_at). On each
-- reassignment the prior officer's row gets ended_at + ended_by;
-- a new row captures the new officer. The collections handler
-- enforces invariant "one open row per case" via partial unique
-- index below.

CREATE TABLE loan_assignment_history (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_id     uuid NOT NULL REFERENCES loan_collection_cases(id) ON DELETE CASCADE,
  loan_id     uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  officer_id  uuid NOT NULL,
  assigned_at timestamptz NOT NULL DEFAULT now(),
  assigned_by uuid NOT NULL,
  ended_at    timestamptz,
  ended_by    uuid,
  end_reason  text
);

CREATE UNIQUE INDEX loan_assignment_history_active_uniq
  ON loan_assignment_history (case_id) WHERE ended_at IS NULL;
CREATE INDEX loan_assignment_history_officer_active_idx
  ON loan_assignment_history (tenant_id, officer_id) WHERE ended_at IS NULL;

ALTER TABLE loan_assignment_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_assignment_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_assignment_history ON loan_assignment_history
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_assignment_history TO nexus_app;

-- ─────────── loan_promises_to_pay — cancel_reason column ───────────
--
-- The legacy 0007 schema's loan_ptp_status enum already includes
-- 'cancelled'; the only gap is no place to record why. The Phase 4
-- ptp/{id}/cancel endpoint writes here + emits a ptp_cancelled event.

ALTER TABLE loan_promises_to_pay
  ADD COLUMN IF NOT EXISTS cancel_reason text;

-- ─────────── collections_escalation_rules ───────────

CREATE TABLE collections_escalation_rules (
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  dpd_min       int NOT NULL,
  dpd_max       int,
  required_role text NOT NULL,
  letter_kind   collections_letter_kind,
  auto_sms      boolean NOT NULL DEFAULT false,
  description   text,
  PRIMARY KEY (tenant_id, dpd_min)
);

ALTER TABLE collections_escalation_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE collections_escalation_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_coll_esc_rules ON collections_escalation_rules
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON collections_escalation_rules TO nexus_app;

INSERT INTO collections_escalation_rules (tenant_id, dpd_min, dpd_max, required_role, letter_kind, auto_sms, description)
SELECT t.id, x.dpd_min, x.dpd_max, x.role, x.letter, x.auto_sms, x."desc"
  FROM tenants t
  CROSS JOIN (VALUES
    (1::int,  6::int,    'credit_officer'::text, NULL::collections_letter_kind,             true,  'Auto SMS reminder day 1; officer monitors'),
    (7,       29,        'credit_officer',       'pre_collection'::collections_letter_kind, false, 'Send pre-collection letter; log calls'),
    (30,      59,        'credit_officer',       'demand'::collections_letter_kind,         false, 'Demand letter + field visit'),
    (60,      89,        'branch_manager',       'final_demand'::collections_letter_kind,   false, 'Branch manager review + final demand'),
    (90,      NULL::int, 'legal',                'legal_notice'::collections_letter_kind,   false, 'Legal team review and recovery action')
  ) AS x(dpd_min, dpd_max, role, letter, auto_sms, "desc")
ON CONFLICT DO NOTHING;

-- ─────────── collections_message_templates ───────────

CREATE TABLE collections_message_templates (
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  channel       text NOT NULL CHECK (channel IN ('sms','email')),
  dpd_min       int  NOT NULL,
  body_template text NOT NULL,
  subject       text,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, channel, dpd_min)
);

ALTER TABLE collections_message_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE collections_message_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_coll_msg_tpl ON collections_message_templates
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON collections_message_templates TO nexus_app;

INSERT INTO collections_message_templates (tenant_id, channel, dpd_min, body_template, subject)
SELECT t.id, 'sms', 1,
  'Hello {{member_name}}, your loan {{loan_no}} at {{tenant_name}} is {{dpd}} day(s) overdue. Outstanding: {{currency}} {{outstanding}}. Please pay to avoid penalties.',
  NULL
  FROM tenants t
ON CONFLICT DO NOTHING;

-- ─────────── loan_doc_kind extension ───────────

ALTER TYPE loan_doc_kind ADD VALUE IF NOT EXISTS 'pre_collection_letter';
ALTER TYPE loan_doc_kind ADD VALUE IF NOT EXISTS 'demand_letter';
ALTER TYPE loan_doc_kind ADD VALUE IF NOT EXISTS 'final_demand_letter';
ALTER TYPE loan_doc_kind ADD VALUE IF NOT EXISTS 'legal_notice_letter';

-- ─────────── tenant_operations.dividend_offset_policy ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS dividend_offset_policy text NOT NULL DEFAULT 'manual_preview'
    CHECK (dividend_offset_policy IN ('disabled','manual_preview','automatic'));

COMMENT ON COLUMN tenant_operations.dividend_offset_policy IS
  'Phase 4 — how dividend payouts handle loan arrears. disabled: full payout regardless. manual_preview: dividend run produces an offset preview; operator posts. automatic: offsets posted inside the dividend run tx.';

-- ─────────── dividend_offset_postings ───────────

CREATE TABLE dividend_offset_postings (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  dividend_run_id  uuid NOT NULL,
  member_id        uuid NOT NULL,
  loan_id          uuid NOT NULL,
  amount           numeric(18,2) NOT NULL,
  allocation       jsonb NOT NULL,
  posted_at        timestamptz NOT NULL DEFAULT now(),
  posted_by        uuid NOT NULL,
  journal_entry_id uuid,
  source_ref       text NOT NULL UNIQUE
);

CREATE INDEX dividend_offset_postings_run_idx
  ON dividend_offset_postings (tenant_id, dividend_run_id);
CREATE INDEX dividend_offset_postings_member_idx
  ON dividend_offset_postings (tenant_id, member_id);

ALTER TABLE dividend_offset_postings ENABLE ROW LEVEL SECURITY;
ALTER TABLE dividend_offset_postings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_div_offset_postings ON dividend_offset_postings
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON dividend_offset_postings TO nexus_app;
