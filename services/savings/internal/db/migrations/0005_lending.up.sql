-- ═══════════════════════════════════════════════════════════════════
-- SAVINGS service — Lending module.
--
-- Loans are SACCO assets (money owed TO the SACCO), the counterpart to
-- the deposit liabilities we already track. They sit in this service
-- because every loan touches: share balance (multiplier basis), deposit
-- balance (multiplier basis, repayment source, disbursement target),
-- and member identity.
--
-- This migration creates the full schema needed by Phases 6a, 6b, and
-- 6c so the downstream handlers don't have to run incremental
-- migrations.
--
-- Tables:
--   • loan_products             — configurable product catalog
--   • loan_purpose_categories   — tenant-customisable purpose list
--   • loan_applications         — submitted by members, scored, approved
--   • loan_guarantees           — per-guarantor row, captures consent
--   • loan_collateral           — pledged assets
--   • loan_documents            — payslips, statements, agreements
--   • loans                     — created on offer acceptance, lives until settled
--   • loan_repayment_schedule   — amortization table; one row per installment
--   • loan_transactions         — append-only ledger (disbursements,
--                                  fees, interest, repayments, penalties)
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Enums ───────────

CREATE TYPE loan_category AS ENUM (
  'short_term', 'medium_term', 'long_term',
  'emergency', 'asset_finance', 'group'
);

CREATE TYPE loan_interest_method AS ENUM ('flat_rate', 'reducing_balance');

CREATE TYPE loan_repayment_method AS ENUM (
  'reducing_balance',   -- amortising; each installment = constant total
  'flat_rate',          -- interest computed on original principal, equal installments
  'bullet',             -- one big payment at maturity
  'interest_only'       -- interest each period, principal at maturity
);

CREATE TYPE loan_fee_timing AS ENUM (
  'upfront',            -- deducted at disbursement
  'added_to_loan',      -- added to principal, spread over installments
  'at_each_installment' -- charged per installment
);

CREATE TYPE loan_collateral_requirement AS ENUM ('required', 'optional', 'not_applicable');

CREATE TYPE loan_multiplier_basis AS ENUM (
  'none', 'shares', 'deposits', 'shares_plus_deposits'
);

CREATE TYPE loan_application_status AS ENUM (
  'draft',                  -- being built; not yet submitted
  'pending_validation',     -- submitted; system validations running
  'pending_guarantor',      -- waiting for guarantor consents
  'pending_scoring',        -- guarantors in; scoring in progress
  'pending_approval',       -- scored; awaiting human or workflow approval
  'approved',               -- approved as applied
  'approved_with_conditions',
  'declined',
  'returned_for_info',
  'offer_sent',             -- offer letter generated; awaiting acceptance
  'offer_accepted',         -- accepted by member; ready to disburse
  'offer_declined',
  'expired',                -- offer not accepted within window
  'cancelled',
  'disbursed'               -- transitioned to a `loans` row
);

CREATE TYPE loan_guarantee_status AS ENUM (
  'pending_consent', 'accepted', 'declined', 'released', 'called_upon'
);

CREATE TYPE loan_collateral_kind AS ENUM (
  'title_deed', 'vehicle_logbook', 'equipment',
  'listed_shares', 'fixed_deposit_lien', 'other'
);

CREATE TYPE loan_doc_kind AS ENUM (
  'payslip', 'bank_statement', 'mpesa_statement',
  'business_financials', 'id_copy',
  'offer_letter_signed', 'agreement', 'other'
);

CREATE TYPE loan_status AS ENUM (
  'pending_disbursement',   -- offer accepted; awaiting disbursement authorisation
  'active',                 -- disbursed; performing
  'in_arrears',             -- DPD > 0
  'defaulted',              -- DPD past the loss threshold
  'restructured',           -- reschedule / top-up / refinance applied
  'settled',                -- fully repaid
  'written_off',            -- recognised as loss
  'closed'                  -- archived
);

CREATE TYPE loan_txn_type AS ENUM (
  'disbursement',           -- + principal to the loan, money OUT of SACCO
  'fee_charge',             -- non-cash; adds to fees outstanding
  'interest_accrual',       -- non-cash; adds to interest outstanding
  'penalty_charge',         -- non-cash; adds to penalty outstanding
  'penalty_waiver',         -- non-cash; reduces penalty outstanding
  'repayment',              -- money IN from member; allocated via waterfall
  'write_off',              -- accept loss; clears outstanding
  'adjustment',             -- administrative correction
  'reversal',               -- inverse of a prior txn
  'settlement_discount'     -- partial write-off accepted as full settlement
);

CREATE TYPE loan_employment_type AS ENUM (
  'salaried', 'self_employed', 'business_owner', 'retired', 'student', 'other'
);

-- ─────────── loan_products ───────────

CREATE TABLE loan_products (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code                        text NOT NULL,
  name                        text NOT NULL,
  category                    loan_category NOT NULL,
  description                 text,
  is_active                   boolean NOT NULL DEFAULT true,

  -- Amount limits
  min_amount                  numeric(18,2) NOT NULL DEFAULT 0,
  max_amount                  numeric(18,2) NOT NULL,
  multiplier_basis            loan_multiplier_basis NOT NULL DEFAULT 'none',
  multiplier_value            numeric(6,3),                       -- e.g. 3 for "3x shares"

  -- Tenure
  min_term_months             int NOT NULL DEFAULT 1,
  max_term_months             int NOT NULL,
  default_term_months         int,
  grace_period_months         int NOT NULL DEFAULT 0,

  -- Interest
  interest_rate_pct           numeric(6,3) NOT NULL,
  interest_method             loan_interest_method NOT NULL DEFAULT 'reducing_balance',
  repayment_method            loan_repayment_method NOT NULL DEFAULT 'reducing_balance',

  -- Fees (3 standard fee slots; expand later via a sub-table if a SACCO has more)
  processing_fee              numeric(18,2) NOT NULL DEFAULT 0,
  processing_fee_is_pct       boolean NOT NULL DEFAULT true,
  processing_fee_timing       loan_fee_timing NOT NULL DEFAULT 'upfront',

  insurance_fee               numeric(18,2) NOT NULL DEFAULT 0,
  insurance_fee_is_pct        boolean NOT NULL DEFAULT true,
  insurance_fee_timing        loan_fee_timing NOT NULL DEFAULT 'upfront',

  appraisal_fee               numeric(18,2) NOT NULL DEFAULT 0,
  appraisal_fee_is_pct        boolean NOT NULL DEFAULT false,
  appraisal_fee_timing        loan_fee_timing NOT NULL DEFAULT 'upfront',

  -- Penalty (used by arrears in Phase 6d, captured here for completeness)
  penalty_rate_pct            numeric(6,3) NOT NULL DEFAULT 0,    -- monthly on overdue principal

  -- Guarantor + collateral requirements
  min_guarantors              int NOT NULL DEFAULT 0,
  max_guarantor_exposure_pct  numeric(5,2) NOT NULL DEFAULT 100,  -- % of guarantor's own (share+deposit)
  guarantor_must_be_member    boolean NOT NULL DEFAULT true,
  collateral_requirement      loan_collateral_requirement NOT NULL DEFAULT 'not_applicable',

  -- Eligibility
  min_membership_months       int NOT NULL DEFAULT 0,
  min_shares_required         int NOT NULL DEFAULT 0,
  allow_concurrent            boolean NOT NULL DEFAULT false,

  -- Approval
  workflow_definition_code    text,                               -- pointer to workflow definition; null = manual
  auto_approval_threshold     numeric(18,2),                      -- if amount <= this and scoring passes, auto-approve
  auto_approval_min_score     int,                                -- minimum credit score for auto-approval

  -- Rollovers / top-ups
  allow_topup                 boolean NOT NULL DEFAULT false,
  allow_refinance             boolean NOT NULL DEFAULT false,

  created_at                  timestamptz NOT NULL DEFAULT now(),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid,
  UNIQUE (tenant_id, code)
);
CREATE INDEX loan_products_tenant_active_idx ON loan_products (tenant_id, is_active, category);
CREATE TRIGGER loan_products_updated_at BEFORE UPDATE ON loan_products
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── loan_purpose_categories ───────────

CREATE TABLE loan_purpose_categories (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code        text NOT NULL,
  name        text NOT NULL,
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

-- ─────────── loan_applications ───────────

CREATE TABLE loan_applications (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_no              text NOT NULL,
  member_id                   uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  product_id                  uuid NOT NULL REFERENCES loan_products(id) ON DELETE RESTRICT,
  status                      loan_application_status NOT NULL DEFAULT 'draft',

  -- Request
  requested_amount            numeric(18,2) NOT NULL,
  requested_term_months       int NOT NULL,
  purpose_category_id         uuid REFERENCES loan_purpose_categories(id) ON DELETE SET NULL,
  purpose_note                text,
  preferred_disbursement_channel text,           -- 'mpesa' | 'bank_transfer' | 'internal' | 'wallet'

  -- Income / affordability inputs (captured by the member or officer)
  employment_type             loan_employment_type,
  employer_name               text,
  employer_payroll_contact    text,
  monthly_net_income          numeric(18,2) NOT NULL DEFAULT 0,
  other_income                numeric(18,2) NOT NULL DEFAULT 0,
  monthly_expenses            numeric(18,2) NOT NULL DEFAULT 0,
  monthly_existing_obligations numeric(18,2) NOT NULL DEFAULT 0,

  -- Scoring output (filled by Phase 6b)
  credit_score                int,
  risk_band                   text,                              -- 'A' | 'B' | 'C' | 'D' | 'Green' | 'Amber' | 'Red'
  affordability_pass          boolean,
  dti_ratio                   numeric(6,3),                      -- proposed_installment / disposable income
  net_disposable_income       numeric(18,2),
  computed_max_amount         numeric(18,2),                     -- multiplier ceiling at scoring time
  computed_max_installment    numeric(18,2),                     -- affordability ceiling
  recommended_amount          numeric(18,2),
  recommended_term_months     int,
  scoring_details             jsonb,                             -- per-factor score breakdown
  scoring_flags               jsonb,                             -- hard blocks + advisories
  scored_at                   timestamptz,

  -- Workflow + approval
  workflow_instance_id        uuid,
  approved_amount             numeric(18,2),
  approved_term_months        int,
  approved_interest_rate_pct  numeric(6,3),
  approved_at                 timestamptz,
  approved_by                 uuid,
  approval_conditions         text,
  decline_category            text,                              -- 'affordability', 'crb', 'fraud', 'incomplete', 'other'
  decline_reason              text,

  -- Offer / acceptance
  offer_letter_path           text,
  offer_sent_at               timestamptz,
  offer_expires_at            timestamptz,
  offer_accepted_at           timestamptz,

  notes                       text,
  created_at                  timestamptz NOT NULL DEFAULT now(),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  created_by                  uuid NOT NULL,
  UNIQUE (tenant_id, application_no)
);
CREATE INDEX loan_apps_tenant_status_idx ON loan_applications (tenant_id, status, created_at DESC);
CREATE INDEX loan_apps_member_idx ON loan_applications (member_id, created_at DESC);
CREATE INDEX loan_apps_workflow_idx ON loan_applications (workflow_instance_id)
  WHERE workflow_instance_id IS NOT NULL;
CREATE TRIGGER loan_apps_updated_at BEFORE UPDATE ON loan_applications
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── loan_guarantees ───────────

CREATE TABLE loan_guarantees (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id      uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id             uuid,                                     -- backfilled on disbursement
  guarantor_member_id uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  amount_guaranteed   numeric(18,2) NOT NULL,
  status              loan_guarantee_status NOT NULL DEFAULT 'pending_consent',
  requested_at        timestamptz NOT NULL DEFAULT now(),
  requested_by        uuid NOT NULL,
  responded_at        timestamptz,
  released_at         timestamptz,
  called_upon_at      timestamptz,
  decline_reason      text,
  notes               text,
  UNIQUE (application_id, guarantor_member_id)
);
CREATE INDEX loan_guarantees_member_idx ON loan_guarantees (guarantor_member_id, status);
CREATE INDEX loan_guarantees_loan_idx ON loan_guarantees (loan_id) WHERE loan_id IS NOT NULL;

-- ─────────── loan_collateral ───────────

CREATE TABLE loan_collateral (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id      uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id             uuid,                                     -- backfilled on disbursement
  kind                loan_collateral_kind NOT NULL,
  description         text NOT NULL,
  estimated_value     numeric(18,2) NOT NULL,
  forced_sale_value   numeric(18,2),
  valuation_date      date,
  valuation_path      text,                                     -- storage path for valuation report
  ownership_path      text,                                     -- storage path for title / logbook
  status              text NOT NULL DEFAULT 'pledged',          -- 'pledged' | 'released' | 'auctioned'
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX loan_collateral_app_idx ON loan_collateral (application_id);
CREATE INDEX loan_collateral_loan_idx ON loan_collateral (loan_id) WHERE loan_id IS NOT NULL;

-- ─────────── loan_documents ───────────

CREATE TABLE loan_documents (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id  uuid REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id         uuid,                                          -- backfilled on disbursement
  kind            loan_doc_kind NOT NULL,
  description     text,
  storage_path    text NOT NULL,
  mime            text NOT NULL,
  size_bytes      bigint NOT NULL,
  uploaded_at     timestamptz NOT NULL DEFAULT now(),
  uploaded_by     uuid
);
CREATE INDEX loan_docs_app_idx ON loan_documents (application_id);
CREATE INDEX loan_docs_loan_idx ON loan_documents (loan_id) WHERE loan_id IS NOT NULL;

-- ─────────── loans ───────────

CREATE TABLE loans (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_no                     text NOT NULL,
  application_id              uuid NOT NULL UNIQUE REFERENCES loan_applications(id) ON DELETE RESTRICT,
  member_id                   uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  product_id                  uuid NOT NULL REFERENCES loan_products(id) ON DELETE RESTRICT,
  status                      loan_status NOT NULL DEFAULT 'pending_disbursement',

  -- Terms snapshot (frozen at offer acceptance time)
  principal                   numeric(18,2) NOT NULL,            -- approved amount
  interest_rate_pct           numeric(6,3)  NOT NULL,
  interest_method             loan_interest_method NOT NULL,
  repayment_method            loan_repayment_method NOT NULL,
  term_months                 int NOT NULL,
  grace_period_months         int NOT NULL DEFAULT 0,
  installment_count           int NOT NULL,
  first_due_date              date,

  -- Disbursement
  disbursement_channel        text,                              -- 'mpesa' | 'bank_transfer' | 'internal' | 'wallet'
  disbursement_target_account_id uuid,                           -- deposit account if channel='internal'
  disbursement_ref            text,
  total_fees_deducted         numeric(18,2) NOT NULL DEFAULT 0,
  net_disbursed               numeric(18,2),                     -- principal − upfront fees
  disbursed_at                timestamptz,
  disbursed_by                uuid,

  -- Cached running balances (the ledger is source of truth)
  principal_disbursed         numeric(18,2) NOT NULL DEFAULT 0,
  principal_repaid            numeric(18,2) NOT NULL DEFAULT 0,
  principal_balance           numeric(18,2) NOT NULL DEFAULT 0,
  interest_charged            numeric(18,2) NOT NULL DEFAULT 0,
  interest_paid               numeric(18,2) NOT NULL DEFAULT 0,
  interest_balance            numeric(18,2) NOT NULL DEFAULT 0,
  fees_charged                numeric(18,2) NOT NULL DEFAULT 0,
  fees_paid                   numeric(18,2) NOT NULL DEFAULT 0,
  fees_balance                numeric(18,2) NOT NULL DEFAULT 0,
  penalty_accrued             numeric(18,2) NOT NULL DEFAULT 0,
  penalty_paid                numeric(18,2) NOT NULL DEFAULT 0,
  penalty_balance             numeric(18,2) NOT NULL DEFAULT 0,

  -- Installment tracking
  installments_paid           int NOT NULL DEFAULT 0,
  next_installment_due_at     date,
  next_installment_amount     numeric(18,2),

  -- Arrears / classification (updated daily by Phase 6d)
  days_past_due               int NOT NULL DEFAULT 0,
  arrears_classification      text NOT NULL DEFAULT 'performing',  -- performing | watch | substandard | doubtful | loss
  last_repayment_at           timestamptz,
  last_arrears_calc_at        timestamptz,

  -- Lifecycle
  created_at                  timestamptz NOT NULL DEFAULT now(),
  updated_at                  timestamptz NOT NULL DEFAULT now(),
  settled_at                  timestamptz,
  written_off_at              timestamptz,
  closed_at                   timestamptz,

  UNIQUE (tenant_id, loan_no)
);
CREATE INDEX loans_member_idx ON loans (member_id, status);
CREATE INDEX loans_status_idx ON loans (tenant_id, status);
CREATE INDEX loans_classification_idx ON loans (tenant_id, arrears_classification) WHERE status IN ('active', 'in_arrears');
CREATE INDEX loans_next_due_idx ON loans (tenant_id, next_installment_due_at) WHERE status IN ('active', 'in_arrears');
CREATE TRIGGER loans_updated_at BEFORE UPDATE ON loans
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── loan_repayment_schedule ───────────

CREATE TABLE loan_repayment_schedule (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id                 uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  installment_no          int NOT NULL,
  due_date                date NOT NULL,
  principal_due           numeric(18,2) NOT NULL,
  interest_due            numeric(18,2) NOT NULL,
  fee_due                 numeric(18,2) NOT NULL DEFAULT 0,
  total_due               numeric(18,2) NOT NULL,
  -- Outputs as payments come in
  principal_paid          numeric(18,2) NOT NULL DEFAULT 0,
  interest_paid           numeric(18,2) NOT NULL DEFAULT 0,
  fee_paid                numeric(18,2) NOT NULL DEFAULT 0,
  status                  text NOT NULL DEFAULT 'pending',         -- 'pending' | 'partially_paid' | 'paid' | 'overdue'
  paid_at                 timestamptz,
  outstanding_after       numeric(18,2) NOT NULL,                  -- principal balance projected after this installment
  UNIQUE (loan_id, installment_no)
);
CREATE INDEX loan_schedule_loan_idx ON loan_repayment_schedule (loan_id, installment_no);
CREATE INDEX loan_schedule_due_idx ON loan_repayment_schedule (tenant_id, due_date) WHERE status IN ('pending', 'partially_paid', 'overdue');

-- ─────────── loan_transactions ───────────

CREATE TABLE loan_transactions (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id                  uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  member_id                uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  txn_no                   text NOT NULL,
  txn_type                 loan_txn_type NOT NULL,
  amount                   numeric(18,2) NOT NULL,                 -- signed: + adds to outstanding, − reduces
  principal_component      numeric(18,2) NOT NULL DEFAULT 0,
  interest_component       numeric(18,2) NOT NULL DEFAULT 0,
  fee_component            numeric(18,2) NOT NULL DEFAULT 0,
  penalty_component        numeric(18,2) NOT NULL DEFAULT 0,
  value_date               date NOT NULL DEFAULT CURRENT_DATE,
  channel                  text,
  channel_ref              text,
  narration                text,
  reverses_txn_id          uuid REFERENCES loan_transactions(id) ON DELETE RESTRICT,
  reversed_by_txn_id       uuid REFERENCES loan_transactions(id) ON DELETE SET NULL,
  installment_no           int,                                    -- which schedule row this applies to (if any)
  posted_at                timestamptz NOT NULL DEFAULT now(),
  initiated_by             uuid NOT NULL,
  authorized_by            uuid,
  UNIQUE (tenant_id, txn_no)
);
CREATE INDEX loan_txn_loan_idx ON loan_transactions (loan_id, posted_at DESC);
CREATE INDEX loan_txn_member_idx ON loan_transactions (member_id, posted_at DESC);
CREATE INDEX loan_txn_tenant_type_idx ON loan_transactions (tenant_id, txn_type, posted_at DESC);

-- ─────────── Tenant-level config ───────────
-- Extend tenant_operations with lending defaults.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS loan_repayment_waterfall text NOT NULL DEFAULT 'penalty,interest,principal,fees',
  ADD COLUMN IF NOT EXISTS dpd_substandard_days int NOT NULL DEFAULT 31,
  ADD COLUMN IF NOT EXISTS dpd_doubtful_days    int NOT NULL DEFAULT 91,
  ADD COLUMN IF NOT EXISTS dpd_loss_days        int NOT NULL DEFAULT 181,
  ADD COLUMN IF NOT EXISTS affordability_dti_threshold_pct numeric(5,2) NOT NULL DEFAULT 40.00,
  ADD COLUMN IF NOT EXISTS affordability_max_installment_pct_of_disposable numeric(5,2) NOT NULL DEFAULT 50.00;

COMMENT ON COLUMN tenant_operations.loan_repayment_waterfall IS
  'Comma-separated order in which repayments allocate: penalty,interest,principal,fees.';

-- ─────────── RLS ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'loan_products', 'loan_purpose_categories', 'loan_applications',
    'loan_guarantees', 'loan_collateral', 'loan_documents',
    'loans', 'loan_repayment_schedule', 'loan_transactions'
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
  loan_products, loan_purpose_categories, loan_applications,
  loan_guarantees, loan_collateral, loan_documents,
  loans, loan_repayment_schedule, loan_transactions
TO nexus_app;
