-- ═══════════════════════════════════════════════════════════════════
-- Collections + restructuring schema (Phase 6e).
--
--   • loan_collection_cases     — one per loan in arrears. Holds
--                                  assignment, priority, last-action,
--                                  total contacts, and lifecycle state.
--   • loan_collection_contacts  — append-only contact attempts.
--                                  Logged each time an officer calls /
--                                  visits / texts the member.
--   • loan_promises_to_pay      — PTPs with kept/broken/partial state.
--                                  Broken PTPs trigger escalation.
--   • loan_restructurings       — audit row per restructuring event,
--                                  capturing previous + new terms.
--   • loan_legal_cases          — stub for legal escalation; deep
--                                  workflow ships in Phase 6f or later.
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Enums ───────────

CREATE TYPE loan_collection_case_status AS ENUM (
  'open', 'in_progress', 'paused', 'escalated_legal', 'closed_recovered', 'closed_uncollectable'
);

CREATE TYPE loan_contact_kind AS ENUM (
  'call', 'sms', 'whatsapp', 'email', 'in_person_visit', 'letter'
);

CREATE TYPE loan_contact_outcome AS ENUM (
  'reached', 'no_answer', 'wrong_number', 'busy',
  'left_message', 'promise_made', 'dispute', 'refused', 'visited_not_home'
);

CREATE TYPE loan_ptp_status AS ENUM ('open', 'kept', 'partial', 'broken', 'cancelled');

CREATE TYPE loan_restructuring_kind AS ENUM (
  'reschedule', 'topup', 'refinance', 'moratorium', 'settlement_discount'
);

CREATE TYPE loan_legal_case_status AS ENUM (
  'demand_letter_sent', 'court_filed', 'judgment', 'execution', 'settled', 'dismissed'
);

-- ─────────── loan_collection_cases ───────────

CREATE TABLE loan_collection_cases (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id                 uuid NOT NULL UNIQUE REFERENCES loans(id) ON DELETE CASCADE,
  member_id               uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  status                  loan_collection_case_status NOT NULL DEFAULT 'open',
  classification_at_open  text,                                   -- snapshot of arrears classification when case opened
  assigned_to             uuid,                                   -- identity user (collections officer)
  assigned_at             timestamptz,
  priority                int  NOT NULL DEFAULT 0,                -- higher = more urgent
  total_contacts          int  NOT NULL DEFAULT 0,
  last_contact_at         timestamptz,
  last_action             text,                                   -- short label of the most recent action
  notes                   text,
  opened_at               timestamptz NOT NULL DEFAULT now(),
  closed_at               timestamptz,
  closed_by               uuid,
  closure_reason          text
);
CREATE INDEX loan_cc_tenant_status_idx ON loan_collection_cases (tenant_id, status, priority DESC, opened_at);
CREATE INDEX loan_cc_assigned_idx ON loan_collection_cases (assigned_to, status)
  WHERE assigned_to IS NOT NULL;

-- ─────────── loan_collection_contacts ───────────

CREATE TABLE loan_collection_contacts (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_id         uuid NOT NULL REFERENCES loan_collection_cases(id) ON DELETE CASCADE,
  kind            loan_contact_kind NOT NULL,
  outcome         loan_contact_outcome NOT NULL,
  note            text,
  gps_lat         numeric(9,6),
  gps_lng         numeric(9,6),
  contacted_at    timestamptz NOT NULL DEFAULT now(),
  contacted_by    uuid NOT NULL
);
CREATE INDEX loan_cc_contacts_case_idx ON loan_collection_contacts (case_id, contacted_at DESC);

-- ─────────── loan_promises_to_pay ───────────

CREATE TABLE loan_promises_to_pay (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_id             uuid NOT NULL REFERENCES loan_collection_cases(id) ON DELETE CASCADE,
  loan_id             uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  promised_amount     numeric(18,2) NOT NULL,
  promised_date       date NOT NULL,
  promised_channel    text,                                   -- 'mpesa' | 'bank' | 'teller' | ...
  status              loan_ptp_status NOT NULL DEFAULT 'open',
  paid_amount         numeric(18,2) NOT NULL DEFAULT 0,
  paid_txn_id         uuid,                                   -- repayment txn that fulfilled (or partially fulfilled) this PTP
  resolved_at         timestamptz,
  resolved_by         uuid,
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid NOT NULL
);
CREATE INDEX loan_ptp_case_idx ON loan_promises_to_pay (case_id, promised_date);
CREATE INDEX loan_ptp_status_date_idx ON loan_promises_to_pay (tenant_id, status, promised_date)
  WHERE status = 'open';

-- ─────────── loan_restructurings ───────────

CREATE TABLE loan_restructurings (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id                     uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  kind                        loan_restructuring_kind NOT NULL,
  reason                      text NOT NULL,

  -- Snapshot of pre-restructuring loan state (for audit + reversal).
  previous_principal_balance  numeric(18,2),
  previous_interest_balance   numeric(18,2),
  previous_term_months        int,
  previous_interest_rate_pct  numeric(6,3),
  previous_repayment_method   loan_repayment_method,
  previous_status             loan_status,

  -- Per-kind parameters:
  new_term_months             int,                            -- reschedule
  new_interest_rate_pct       numeric(6,3),                   -- reschedule / refinance
  topup_amount                numeric(18,2),                  -- topup
  refinance_new_loan_id       uuid REFERENCES loans(id) ON DELETE SET NULL,  -- refinance
  moratorium_months           int,                            -- moratorium
  moratorium_suspend_interest boolean,                        -- moratorium
  discount_amount             numeric(18,2),                  -- settlement_discount
  discount_writeoff_txn_id    uuid REFERENCES loan_transactions(id) ON DELETE SET NULL,

  -- Workflow integration (extension point — direct-approve when null).
  workflow_instance_id        uuid,
  authorized_at               timestamptz,
  authorized_by               uuid,

  created_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid NOT NULL
);
CREATE INDEX loan_restructurings_loan_idx ON loan_restructurings (loan_id, created_at DESC);
CREATE INDEX loan_restructurings_kind_idx ON loan_restructurings (tenant_id, kind, created_at DESC);

-- ─────────── loan_legal_cases (skeleton for Phase 6f / later) ───────────

CREATE TABLE loan_legal_cases (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id             uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  collection_case_id  uuid REFERENCES loan_collection_cases(id) ON DELETE SET NULL,
  legal_firm          text NOT NULL,
  case_reference      text,
  instruction_date    date NOT NULL,
  next_court_date     date,
  status              loan_legal_case_status NOT NULL DEFAULT 'demand_letter_sent',
  legal_fees_incurred numeric(18,2) NOT NULL DEFAULT 0,
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid NOT NULL
);
CREATE INDEX loan_legal_loan_idx ON loan_legal_cases (loan_id, created_at DESC);

-- ─────────── RLS ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'loan_collection_cases', 'loan_collection_contacts',
    'loan_promises_to_pay', 'loan_restructurings', 'loan_legal_cases'
  ])
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

-- ─────────── Grants ───────────

GRANT SELECT, INSERT, UPDATE, DELETE ON
  loan_collection_cases, loan_collection_contacts,
  loan_promises_to_pay, loan_restructurings, loan_legal_cases
TO nexus_app;
