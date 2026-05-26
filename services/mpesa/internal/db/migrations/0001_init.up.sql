-- mpesa service — initial schema (§3.4 in the M-PESA integration spec).
--
-- Seven tables, all RLS-scoped by tenant_id:
--   mpesa_paybills
--   mpesa_paybill_credentials
--   mpesa_distribution_policies
--   mpesa_inbound_events
--   mpesa_distribution_runs
--   mpesa_outbound_requests
--   mpesa_reversal_events
--
-- The event / outbound / reversal tables are schema-only in this PR
-- (no handlers write to them yet). Indexes + RLS are in place so
-- phases 2–5 can plug in without another migration churn.
--
-- The credentials table is special: nexus_app does NOT get a generic
-- SELECT grant on it. Reads go through the SECURITY DEFINER function
-- mpesa_credentials_read(paybill_id, kind) so a `SELECT *` from the
-- app role can't enumerate or exfiltrate ciphertexts. INSERT + UPDATE
-- + DELETE remain grant-level so the credential CRUD handler works
-- via normal pgx writes.

-- ─────────── Enums ───────────
CREATE TYPE mpesa_environment AS ENUM ('sandbox', 'production');
CREATE TYPE mpesa_paybill_purpose AS ENUM ('collection', 'disbursement', 'both');
CREATE TYPE mpesa_paybill_status AS ENUM ('active', 'disabled');
CREATE TYPE mpesa_credential_kind AS ENUM (
  'consumer_key', 'consumer_secret', 'passkey',
  'initiator_name', 'initiator_password'
);
CREATE TYPE mpesa_resolver_status AS ENUM ('pending', 'matched', 'unmatched', 'failed');
CREATE TYPE mpesa_distribution_status AS ENUM ('pending', 'posted', 'failed');
CREATE TYPE mpesa_outbound_kind AS ENUM ('b2c_disbursement', 'refund');
CREATE TYPE mpesa_outbound_status AS ENUM ('pending', 'sent', 'completed', 'failed');
CREATE TYPE mpesa_reversal_direction AS ENUM ('inbound', 'outbound');
CREATE TYPE mpesa_reversal_status AS ENUM ('pending', 'completed', 'failed');

-- ─────────── mpesa_paybills ───────────
CREATE TABLE mpesa_paybills (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  label                   text NOT NULL,
  shortcode               text NOT NULL,
  purpose                 mpesa_paybill_purpose NOT NULL,
  scope                   text[] NOT NULL DEFAULT '{}',
  environment             mpesa_environment NOT NULL,
  status                  mpesa_paybill_status NOT NULL DEFAULT 'active',
  -- soft link to mpesa_distribution_policies(id); not a FK because the
  -- policy may be authored after the paybill (and may legitimately be
  -- removed without orphaning the paybill row).
  distribution_policy_id  uuid,
  created_by              uuid,
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, shortcode, environment)
);
CREATE INDEX mpesa_paybills_tenant_idx ON mpesa_paybills (tenant_id, status);

-- ─────────── mpesa_paybill_credentials ───────────
CREATE TABLE mpesa_paybill_credentials (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  paybill_id  uuid NOT NULL REFERENCES mpesa_paybills(id) ON DELETE CASCADE,
  kind        mpesa_credential_kind NOT NULL,
  key_id      text NOT NULL,                 -- KMS master-key id stamped into ciphertext header
  ciphertext  bytea NOT NULL,
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (paybill_id, kind)
);

-- ─────────── mpesa_distribution_policies ───────────
CREATE TABLE mpesa_distribution_policies (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name        text NOT NULL,
  description text,
  waterfall   jsonb NOT NULL,                -- ordered [{target,percent_or_amount,conditions}]
  status      text NOT NULL DEFAULT 'active',
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

-- ─────────── mpesa_inbound_events (schema-only this PR) ───────────
CREATE TABLE mpesa_inbound_events (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  paybill_id         uuid REFERENCES mpesa_paybills(id), -- nullable when shortcode unresolved
  shortcode          text NOT NULL,
  transaction_id     text NOT NULL,         -- Safaricom MpesaReceiptNumber
  transaction_time   timestamptz,
  amount             numeric(18,2) NOT NULL,
  msisdn             text,
  bill_ref           text,                  -- BillRefNumber the user typed
  raw_payload        jsonb NOT NULL,
  resolver_status    mpesa_resolver_status NOT NULL DEFAULT 'pending',
  resolver_decision  jsonb,
  resolved_at        timestamptz,
  received_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, transaction_id)
);
CREATE INDEX mpesa_inbound_events_paybill_idx
  ON mpesa_inbound_events (tenant_id, paybill_id, received_at DESC);
CREATE INDEX mpesa_inbound_events_resolver_idx
  ON mpesa_inbound_events (tenant_id, resolver_status, received_at DESC);

-- ─────────── mpesa_distribution_runs (schema-only) ───────────
CREATE TABLE mpesa_distribution_runs (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  inbound_event_id    uuid NOT NULL REFERENCES mpesa_inbound_events(id) ON DELETE CASCADE,
  policy_id           uuid REFERENCES mpesa_distribution_policies(id),
  splits              jsonb NOT NULL,        -- [{target,amount,target_ref}]
  status              mpesa_distribution_status NOT NULL DEFAULT 'pending',
  posting_journal_id  uuid,                  -- soft ref to accounting.journal_entries
  error               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  posted_at           timestamptz
);
CREATE INDEX mpesa_distribution_runs_event_idx
  ON mpesa_distribution_runs (tenant_id, inbound_event_id);

-- ─────────── mpesa_outbound_requests (schema-only) ───────────
CREATE TABLE mpesa_outbound_requests (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  paybill_id               uuid NOT NULL REFERENCES mpesa_paybills(id),
  kind                     mpesa_outbound_kind NOT NULL,
  msisdn                   text NOT NULL,
  amount                   numeric(18,2) NOT NULL,
  source_module            text NOT NULL,    -- 'loan'|'savings'|… for cross-service idempotency
  source_ref               text NOT NULL,
  status                   mpesa_outbound_status NOT NULL DEFAULT 'pending',
  daraja_conversation_id   text,
  daraja_originator_id     text,
  result_code              text,
  result_desc              text,
  requested_at             timestamptz NOT NULL DEFAULT now(),
  sent_at                  timestamptz,
  completed_at             timestamptz,
  UNIQUE (tenant_id, source_module, source_ref)
);
CREATE INDEX mpesa_outbound_requests_status_idx
  ON mpesa_outbound_requests (tenant_id, status, requested_at);

-- ─────────── mpesa_reversal_events (schema-only) ───────────
CREATE TABLE mpesa_reversal_events (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  direction     mpesa_reversal_direction NOT NULL,
  original_id   uuid,                        -- soft ref to inbound or outbound
  transaction_id text,
  amount        numeric(18,2),
  reason        text,
  raw_payload   jsonb,
  status        mpesa_reversal_status NOT NULL DEFAULT 'pending',
  received_at   timestamptz NOT NULL DEFAULT now(),
  completed_at  timestamptz
);

-- ─────────── RLS ───────────
ALTER TABLE mpesa_paybills                  ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_paybills                  FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_paybill_credentials       ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_paybill_credentials       FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_distribution_policies     ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_distribution_policies     FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_inbound_events            ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_inbound_events            FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_distribution_runs         ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_distribution_runs         FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_outbound_requests         ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_outbound_requests         FORCE ROW LEVEL SECURITY;
ALTER TABLE mpesa_reversal_events           ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_reversal_events           FORCE ROW LEVEL SECURITY;

-- current_tenant_id() is defined in the identity bootstrap migration
-- — every tenant-scoped table across the platform uses the same
-- helper. Re-using it keeps every service's RLS behaviour identical.
CREATE POLICY tenant_isolation_mpesa_paybills              ON mpesa_paybills              USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_paybill_credentials   ON mpesa_paybill_credentials   USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_distribution_policies ON mpesa_distribution_policies USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_inbound_events        ON mpesa_inbound_events        USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_distribution_runs     ON mpesa_distribution_runs     USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_outbound_requests     ON mpesa_outbound_requests     USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_reversal_events       ON mpesa_reversal_events       USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

-- ─────────── Grants ───────────
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_paybills              TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_distribution_policies TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_inbound_events        TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_distribution_runs     TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_outbound_requests     TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_reversal_events       TO nexus_app;

-- mpesa_paybill_credentials: INSERT/UPDATE/DELETE only, NO SELECT.
-- Reads go through the SECURITY DEFINER function below so an ad-hoc
-- `SELECT * FROM mpesa_paybill_credentials` from the app role fails
-- with a permission error rather than returning ciphertexts.
GRANT INSERT, UPDATE, DELETE ON mpesa_paybill_credentials TO nexus_app;

-- ─────────── SECURITY DEFINER credential read function ───────────
-- Returns the most recent (paybill_id, kind) row's key_id + ciphertext.
-- The function is owned by `nexus` (the privileged role from the
-- bootstrap migration), so it can SELECT past the missing app-role
-- grant. SECURITY DEFINER + a RAISE on tenant mismatch means the
-- function can't be used as an exfiltration tool from inside a
-- compromised handler that forgot to set app.tenant_id.
CREATE OR REPLACE FUNCTION mpesa_credentials_read(
  p_paybill_id uuid,
  p_kind       mpesa_credential_kind
)
RETURNS TABLE (key_id text, ciphertext bytea)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
  v_tenant uuid := current_tenant_id();
BEGIN
  IF v_tenant IS NULL THEN
    RAISE EXCEPTION 'mpesa_credentials_read: tenant scope is not set' USING ERRCODE = '28000';
  END IF;
  RETURN QUERY
    SELECT c.key_id, c.ciphertext
      FROM mpesa_paybill_credentials c
     WHERE c.paybill_id = p_paybill_id
       AND c.kind       = p_kind
       AND c.tenant_id  = v_tenant;
END;
$$;

-- Lock down EXECUTE so only the app role can invoke it.
REVOKE ALL ON FUNCTION mpesa_credentials_read(uuid, mpesa_credential_kind) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION mpesa_credentials_read(uuid, mpesa_credential_kind) TO nexus_app;

COMMENT ON FUNCTION mpesa_credentials_read(uuid, mpesa_credential_kind) IS
  'Canonical credential read path for mpesa_paybill_credentials. nexus_app has no direct SELECT on the ciphertext table; all reads route through this SECURITY DEFINER function so tenant scope is enforced and bulk-exfiltration via SELECT * is impossible.';
