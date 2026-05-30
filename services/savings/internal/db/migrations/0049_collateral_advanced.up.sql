-- Phase 1.5b — Collateral advanced features.
--
-- Builds on 0048_collateral_foundation. Adds:
--   1. Third-party pledger columns + consent SMS plumbing.
--   2. Real liens on internal accounts (deposit + share).
--   3. Charge registration tracking (legal filing references).
--   4. Insurance tracking with expiry-driven workflow.
--   5. Document custody chain-of-possession.
--   6. Auction event log for disposal path.
--   7. Tenant-policy knobs that drive the approval gate's
--      "charge required" + "insurance required" sub-checks.
--
-- See nexusSacco_Loans_Phase_1_5b_Collateral_Advanced_Prompt.md.

BEGIN;

-- ─────────── 1. Third-party pledger on loan_collateral ───────────

ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS pledger_counterparty_id  uuid REFERENCES counterparties(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS pledger_consent_status   text
    CHECK (pledger_consent_status IS NULL OR pledger_consent_status IN ('pending','accepted','declined','offline_consented')),
  ADD COLUMN IF NOT EXISTS pledger_consent_at       timestamptz,
  ADD COLUMN IF NOT EXISTS pledger_consent_doc_path text;

COMMENT ON COLUMN loan_collateral.pledger_counterparty_id IS
  'NULL = self-pledge by the borrower. Non-NULL = third-party pledger; consent flow required before pledged state.';

-- Speed up "every loan_collateral this counterparty pledged" — drives
-- the Member 360 → "Pledges given" tab.
CREATE INDEX IF NOT EXISTS loan_collateral_pledger_idx
  ON loan_collateral (pledger_counterparty_id)
  WHERE pledger_counterparty_id IS NOT NULL;

-- ─────────── 2. Deposit lien linkage ───────────
--
-- One row per (collateral_id) — internal_deposit_lien kind only. The
-- liened_amount is the portion of the deposit account's balance locked
-- against this loan. Status walks active → released or active → exercised
-- (proceeds applied to loan).

CREATE TABLE collateral_deposit_liens (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  deposit_account_id  uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  liened_amount       numeric(18,2) NOT NULL CHECK (liened_amount > 0),
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','partially_released','released','exercised')),
  placed_at           timestamptz NOT NULL DEFAULT now(),
  placed_by           uuid NOT NULL,
  released_at         timestamptz,
  released_by         uuid,
  released_reason     text,
  exercised_at        timestamptz,
  exercised_by        uuid,
  exercise_reason     text,
  UNIQUE (collateral_id)
);
CREATE INDEX collateral_deposit_liens_account_active_idx
  ON collateral_deposit_liens (deposit_account_id)
  WHERE status IN ('active','partially_released');

ALTER TABLE collateral_deposit_liens ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_deposit_liens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_deposit_liens ON collateral_deposit_liens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON collateral_deposit_liens TO nexus_app;

-- ─────────── Share pledge linkage ───────────
--
-- Mirror of collateral_deposit_liens, but for share_accounts.
-- Independent from the existing share_liens table (which Phase 5
-- introduced for BOSA-specific exit liens). Both contribute to the
-- "pledged shares" total the share transfer/redeem gate checks.

CREATE TABLE collateral_share_pledges (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  share_account_id    uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  pledged_share_count int  NOT NULL CHECK (pledged_share_count > 0),
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','partially_released','released','exercised')),
  placed_at           timestamptz NOT NULL DEFAULT now(),
  placed_by           uuid NOT NULL,
  released_at         timestamptz,
  released_by         uuid,
  released_reason     text,
  exercised_at        timestamptz,
  exercised_by        uuid,
  exercise_reason     text,
  UNIQUE (collateral_id)
);
CREATE INDEX collateral_share_pledges_account_active_idx
  ON collateral_share_pledges (share_account_id)
  WHERE status IN ('active','partially_released');

ALTER TABLE collateral_share_pledges ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_share_pledges FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_share_pledges ON collateral_share_pledges
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON collateral_share_pledges TO nexus_app;

-- ─────────── 3. Charge registration (legal filing) ───────────

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'charge_registry') THEN
    CREATE TYPE charge_registry AS ENUM (
      'lands_registry', 'ntsa', 'stockbroker_custodian', 'kra', 'other'
    );
  END IF;
END $$;

ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS charge_registry         charge_registry,
  ADD COLUMN IF NOT EXISTS charge_reference        text,
  ADD COLUMN IF NOT EXISTS charge_registered_at    timestamptz,
  ADD COLUMN IF NOT EXISTS charge_registered_by    uuid,
  ADD COLUMN IF NOT EXISTS charge_discharge_ref    text,
  ADD COLUMN IF NOT EXISTS charge_discharged_at    timestamptz,
  ADD COLUMN IF NOT EXISTS charge_certificate_path text;

-- ─────────── 4. Insurance policies (one current per item) ───────────

CREATE TABLE collateral_insurance_policies (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  provider_name       text NOT NULL,
  policy_no           text NOT NULL,
  effective_from      date NOT NULL,
  effective_to        date NOT NULL,
  premium_amount      numeric(18,2),
  sum_insured         numeric(18,2) NOT NULL CHECK (sum_insured > 0),
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','expired','cancelled')),
  is_current          boolean NOT NULL DEFAULT true,
  policy_doc_path     text,
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid NOT NULL,
  CHECK (effective_to >= effective_from)
);

CREATE INDEX collateral_insurance_expiring_idx
  ON collateral_insurance_policies (tenant_id, effective_to)
  WHERE is_current = true AND status = 'active';
CREATE UNIQUE INDEX collateral_insurance_one_current_idx
  ON collateral_insurance_policies (collateral_id)
  WHERE is_current = true;

ALTER TABLE collateral_insurance_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_insurance_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_insurance ON collateral_insurance_policies
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON collateral_insurance_policies TO nexus_app;

-- ─────────── 5. Document custody (chain of possession) ───────────

CREATE TABLE collateral_document_custody (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id           uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  document_kind           text NOT NULL,
  movement                text NOT NULL CHECK (movement IN ('checked_in','checked_out','returned_to_borrower')),
  movement_at             timestamptz NOT NULL DEFAULT now(),
  movement_by             uuid NOT NULL,
  custodian_user_id       uuid,
  borrower_signature_path text,
  location_code           text,
  notes                   text
);

CREATE INDEX collateral_document_custody_collateral_idx
  ON collateral_document_custody (collateral_id, movement_at DESC);

ALTER TABLE collateral_document_custody ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_document_custody FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_document_custody ON collateral_document_custody
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON collateral_document_custody TO nexus_app;

-- ─────────── 6. Auction events ───────────

CREATE TABLE collateral_auction_events (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  loan_id             uuid REFERENCES loans(id) ON DELETE SET NULL,
  event_kind          text NOT NULL CHECK (event_kind IN (
    'handover_to_auctioneer','auction_notice_published','auction_held',
    'sold','reserve_not_met','rescheduled','proceeds_received'
  )),
  occurred_at         timestamptz NOT NULL DEFAULT now(),
  amount              numeric(18,2),
  buyer_details       text,
  auctioneer_name     text,
  notes               text,
  doc_path            text,
  created_by          uuid NOT NULL,
  CHECK (
    (event_kind IN ('sold','proceeds_received') AND amount IS NOT NULL AND amount > 0)
    OR event_kind NOT IN ('sold','proceeds_received')
  )
);

CREATE INDEX collateral_auction_events_loan_idx
  ON collateral_auction_events (loan_id, occurred_at DESC);
CREATE INDEX collateral_auction_events_collateral_idx
  ON collateral_auction_events (collateral_id, occurred_at DESC);

ALTER TABLE collateral_auction_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_auction_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_auction_events ON collateral_auction_events
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON collateral_auction_events TO nexus_app;

-- ─────────── 7. Third-party pledger consent tokens ───────────
--
-- Mirror of guarantor_consent_tokens (migration 0047). One row per
-- (collateral, attempt). SECURITY DEFINER helper bypasses RLS for the
-- public route's initial token-to-tenant lookup.

CREATE TABLE collateral_pledger_consent_tokens (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id            uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  token_hash               bytea NOT NULL UNIQUE,
  attempt_number           int NOT NULL DEFAULT 1 CHECK (attempt_number > 0),
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid NOT NULL,
  expires_at               timestamptz NOT NULL,
  used_at                  timestamptz,
  -- OTP state mirrors guarantor_consent_tokens — single row carries both.
  otp_code_hash            bytea,
  otp_sent_to              text,
  otp_expires_at           timestamptz,
  otp_attempts             int NOT NULL DEFAULT 0,
  otp_verified_at          timestamptz,
  -- Decision payload + audit.
  decision                 text CHECK (decision IS NULL OR decision IN ('accepted','declined','opted_offline','abandoned')),
  decision_reason          text,
  decision_signature_path  text,
  ip_address               inet,
  user_agent               text
);

CREATE INDEX collateral_pledger_consent_tokens_collateral_idx
  ON collateral_pledger_consent_tokens (collateral_id, created_at DESC);
CREATE INDEX collateral_pledger_consent_tokens_due_idx
  ON collateral_pledger_consent_tokens (tenant_id, decision)
  WHERE decision IS NULL AND used_at IS NULL;

ALTER TABLE collateral_pledger_consent_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE collateral_pledger_consent_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_pledger_consent_tokens ON collateral_pledger_consent_tokens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON collateral_pledger_consent_tokens TO nexus_app;

-- SECURITY DEFINER bridge — the public route hashes the URL token and
-- needs to discover the tenant before it can set app.tenant_id for
-- normal RLS-scoped reads. Mirrors find_guarantor_token_tenant().
CREATE OR REPLACE FUNCTION find_pledger_token_tenant(p_hash bytea)
RETURNS TABLE (token_id uuid, tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT id, tenant_id
    FROM collateral_pledger_consent_tokens
   WHERE token_hash = p_hash
   LIMIT 1
$$;
REVOKE ALL ON FUNCTION find_pledger_token_tenant(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_pledger_token_tenant(bytea) TO nexus_app;

-- ─────────── 8. Tenant-policy knobs ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS collateral_charge_required_kinds    text[] NOT NULL DEFAULT ARRAY['title_deed','vehicle_logbook'],
  ADD COLUMN IF NOT EXISTS collateral_insurance_required_kinds text[] NOT NULL DEFAULT ARRAY['title_deed','vehicle_logbook','equipment'],
  ADD COLUMN IF NOT EXISTS collateral_revaluation_warning_days int NOT NULL DEFAULT 60
    CHECK (collateral_revaluation_warning_days BETWEEN 1 AND 365),
  ADD COLUMN IF NOT EXISTS collateral_insurance_warning_days   int NOT NULL DEFAULT 30
    CHECK (collateral_insurance_warning_days BETWEEN 1 AND 365);

COMMIT;
