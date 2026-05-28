-- Loans Phase 5 — top-up + refinance + check-off + group loans + BOSA
-- liens + insider flag.
--
-- One migration covers all six features. Design notes inline.

-- ─────────── loan_applications extensions ───────────
--
-- application_type:   new (default) / topup / refinance
-- parent_loan_id:     top-up reference; refinance uses the largest
--                     source loan OR NULL when multiple (then see
--                     refinance_source_loan_ids).
-- refinance_source_loan_ids: jsonb array of uuid strings for multi-loan
--                     consolidation refinance. NULL for top-up + single-
--                     source refinance + new applications.
-- applicant_kind:     individual (default) / group. Drives group fields.
-- borrower_counterparty_id: institutional counterparty for group apps.
--                     For individual apps stays NULL — the existing
--                     member_id FK identifies the borrower. For group
--                     apps both columns are set: member_id is the chair
--                     officer (so existing queries that pivot off
--                     member_id still work), borrower_counterparty_id
--                     points at the institution.
-- group_income_source: free-form text for the group's income basis.
-- is_insider/insider_category: see Phase 5 step 6.

ALTER TABLE loan_applications
  ADD COLUMN IF NOT EXISTS application_type text NOT NULL DEFAULT 'new'
    CHECK (application_type IN ('new','topup','refinance')),
  ADD COLUMN IF NOT EXISTS parent_loan_id uuid REFERENCES loans(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS refinance_source_loan_ids jsonb,
  ADD COLUMN IF NOT EXISTS applicant_kind text NOT NULL DEFAULT 'individual'
    CHECK (applicant_kind IN ('individual','group')),
  ADD COLUMN IF NOT EXISTS borrower_counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS group_income_source text,
  ADD COLUMN IF NOT EXISTS is_insider boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS insider_category text
    CHECK (insider_category IS NULL OR insider_category IN ('staff','board','committee','spouse_of_insider','related_party'));

CREATE INDEX IF NOT EXISTS loan_apps_parent_loan_idx
  ON loan_applications (parent_loan_id) WHERE parent_loan_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS loan_apps_borrower_cp_idx
  ON loan_applications (borrower_counterparty_id) WHERE borrower_counterparty_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS loan_apps_insider_idx
  ON loan_applications (tenant_id, is_insider) WHERE is_insider;
CREATE INDEX IF NOT EXISTS loan_apps_app_type_idx
  ON loan_applications (tenant_id, application_type) WHERE application_type <> 'new';


-- ─────────── loans — insider flag ───────────

ALTER TABLE loans
  ADD COLUMN IF NOT EXISTS is_insider boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS insider_category text
    CHECK (insider_category IS NULL OR insider_category IN ('staff','board','committee','spouse_of_insider','related_party'));

CREATE INDEX IF NOT EXISTS loans_insider_idx
  ON loans (tenant_id, is_insider) WHERE is_insider;


-- ─────────── Group loans — officer consents + apportionment ───────────

CREATE TABLE IF NOT EXISTS loan_group_officer_consents (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id  uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id         uuid REFERENCES loans(id) ON DELETE SET NULL,
  officer_member_id uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  position        text NOT NULL,                            -- chair | treasurer | secretary | signatory
  status          text NOT NULL DEFAULT 'pending_consent'
    CHECK (status IN ('pending_consent','consented','declined')),
  responded_at    timestamptz,
  decline_reason  text
);
CREATE INDEX IF NOT EXISTS lgoc_application_idx ON loan_group_officer_consents (application_id);
CREATE INDEX IF NOT EXISTS lgoc_loan_idx ON loan_group_officer_consents (loan_id) WHERE loan_id IS NOT NULL;
ALTER TABLE loan_group_officer_consents ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_group_officer_consents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_group_officer_consents ON loan_group_officer_consents
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_group_officer_consents TO nexus_app;

CREATE TABLE IF NOT EXISTS loan_group_apportionment (
  loan_id    uuid NOT NULL REFERENCES loans(id) ON DELETE CASCADE,
  member_id  uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  share_pct  numeric(5,2) NOT NULL CHECK (share_pct > 0 AND share_pct <= 100),
  PRIMARY KEY (loan_id, member_id)
);
ALTER TABLE loan_group_apportionment ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_group_apportionment FORCE ROW LEVEL SECURITY;
-- Apportionment is read by the same tenant as the loan; we scope via
-- the loan_id join in the policy to keep RLS strict.
CREATE POLICY tenant_isolation_loan_group_apportionment ON loan_group_apportionment
  USING (EXISTS (SELECT 1 FROM loans l WHERE l.id = loan_id AND l.tenant_id = current_tenant_id()))
  WITH CHECK (EXISTS (SELECT 1 FROM loans l WHERE l.id = loan_id AND l.tenant_id = current_tenant_id()));
GRANT SELECT, INSERT, UPDATE, DELETE ON loan_group_apportionment TO nexus_app;

-- Trigger: total share_pct per loan must be <= 100.00 (= 100 enforced
-- by the handler; trigger is the defensive backstop).
CREATE OR REPLACE FUNCTION enforce_group_apportionment_sum() RETURNS trigger AS $$
DECLARE total numeric;
BEGIN
  SELECT COALESCE(SUM(share_pct), 0) INTO total
    FROM loan_group_apportionment WHERE loan_id = NEW.loan_id;
  IF total > 100.00 THEN
    RAISE EXCEPTION 'loan_group_apportionment: share_pct sums to %, must be <= 100', total;
  END IF;
  RETURN NEW;
END $$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS loan_group_apportionment_sum_check ON loan_group_apportionment;
CREATE TRIGGER loan_group_apportionment_sum_check
  AFTER INSERT OR UPDATE ON loan_group_apportionment
  FOR EACH ROW EXECUTE FUNCTION enforce_group_apportionment_sum();


-- ─────────── Salary check-off batches ───────────

CREATE TABLE IF NOT EXISTS checkoff_batches (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  employer_name    text NOT NULL,
  employer_code    text,
  period_label     text NOT NULL,
  upload_filename  text NOT NULL,
  uploaded_at      timestamptz NOT NULL DEFAULT now(),
  uploaded_by      uuid NOT NULL,
  status           text NOT NULL DEFAULT 'draft'
    CHECK (status IN ('draft','validated','posted','partial','failed','cancelled')),
  row_count        int NOT NULL DEFAULT 0,
  matched_count    int NOT NULL DEFAULT 0,
  unmatched_count  int NOT NULL DEFAULT 0,
  posted_amount    numeric(18,2) NOT NULL DEFAULT 0,
  unmatched_amount numeric(18,2) NOT NULL DEFAULT 0,
  posted_at        timestamptz,
  posted_by        uuid,
  notes            text
);
CREATE INDEX IF NOT EXISTS checkoff_batches_tenant_status_idx
  ON checkoff_batches (tenant_id, status, uploaded_at DESC);
ALTER TABLE checkoff_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkoff_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_checkoff_batches ON checkoff_batches
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON checkoff_batches TO nexus_app;

CREATE TABLE IF NOT EXISTS checkoff_batch_rows (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  batch_id           uuid NOT NULL REFERENCES checkoff_batches(id) ON DELETE CASCADE,
  row_no             int NOT NULL,
  member_no_raw      text NOT NULL,
  amount_raw         text NOT NULL,
  resolved_member_id uuid,
  resolved_loan_id   uuid,
  amount             numeric(18,2),
  status             text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending','matched','ambiguous','unmatched','posted','failed','skipped')),
  error_message      text,
  posted_txn_id      uuid,
  UNIQUE (batch_id, row_no)
);
CREATE INDEX IF NOT EXISTS checkoff_batch_rows_status_idx
  ON checkoff_batch_rows (batch_id, status);
ALTER TABLE checkoff_batch_rows ENABLE ROW LEVEL SECURITY;
ALTER TABLE checkoff_batch_rows FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_checkoff_batch_rows ON checkoff_batch_rows
  USING (EXISTS (SELECT 1 FROM checkoff_batches b WHERE b.id = batch_id AND b.tenant_id = current_tenant_id()))
  WITH CHECK (EXISTS (SELECT 1 FROM checkoff_batches b WHERE b.id = batch_id AND b.tenant_id = current_tenant_id()));
GRANT SELECT, INSERT, UPDATE, DELETE ON checkoff_batch_rows TO nexus_app;


-- ─────────── BOSA liens ───────────

CREATE TABLE IF NOT EXISTS bosa_liens (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  bosa_account_id uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  loan_id         uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  member_id       uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  amount          numeric(18,2) NOT NULL,
  status          text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','partially_released','released','exercised')),
  placed_at       timestamptz NOT NULL DEFAULT now(),
  placed_by       uuid NOT NULL,
  released_at     timestamptz,
  released_by     uuid,
  exercised_at    timestamptz,
  exercised_by    uuid,
  exercise_reason text,
  UNIQUE (loan_id)
);
CREATE INDEX IF NOT EXISTS bosa_liens_bosa_active_idx
  ON bosa_liens (bosa_account_id) WHERE status IN ('active','partially_released');
CREATE INDEX IF NOT EXISTS bosa_liens_member_active_idx
  ON bosa_liens (tenant_id, member_id) WHERE status IN ('active','partially_released');
ALTER TABLE bosa_liens ENABLE ROW LEVEL SECURITY;
ALTER TABLE bosa_liens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_bosa_liens ON bosa_liens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON bosa_liens TO nexus_app;


-- ─────────── tenant_operations — BOSA lien release policy ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS bosa_lien_release_policy text NOT NULL DEFAULT 'on_settlement'
    CHECK (bosa_lien_release_policy IN ('on_settlement','proportional'));

COMMENT ON COLUMN tenant_operations.bosa_lien_release_policy IS
  'Phase 5 — on_settlement: lien stays full until loan settles. proportional: lien amount reduces as principal is repaid (deferred to follow-up; placeholder).';
