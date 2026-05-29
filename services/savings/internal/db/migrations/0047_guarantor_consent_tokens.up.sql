-- Phase 5 follow-up — SMS-based guarantor consent flow.
--
-- Each guarantor row inserted into loan_guarantees gets a paired
-- guarantor_consent_tokens row + an SMS containing a public link
-- https://{tenant-slug}.../g/{token}.
--
-- Token security: stored hashed (sha256), single-use, time-bound (default
-- 7 days), per-IP and per-token rate-limited in the public endpoint
-- layer. ID + OTP verification on the public page proves the page
-- visitor is the guarantor; OTP is sent to the guarantor's
-- members.phone (not the visitor's claim).
--
-- The token row also carries the OTP state to avoid a second table:
-- otp_code_hash, otp_expires_at, otp_attempts. When max attempts is
-- exceeded the row is poisoned (used_at set, decision='abandoned').

CREATE TABLE IF NOT EXISTS guarantor_consent_tokens (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  guarantee_id            uuid NOT NULL REFERENCES loan_guarantees(id) ON DELETE CASCADE,
  -- sha256 of the URL-safe plaintext. The plaintext only ever exists
  -- in the SMS body and the visitor's URL bar; we never store it.
  token_hash              bytea NOT NULL UNIQUE,
  attempt_number          int NOT NULL DEFAULT 1,        -- 1=original, 2=first reminder, 3=second reminder
  created_at              timestamptz NOT NULL DEFAULT now(),
  created_by              uuid NOT NULL,
  expires_at              timestamptz NOT NULL,
  used_at                 timestamptz,                    -- single-use marker
  decision                text CHECK (decision IS NULL OR decision IN
                                      ('accepted','declined','opted_offline','abandoned')),
  decision_reason         text,
  decision_signature_path text,
  ip_address              inet,
  user_agent              text,
  -- OTP state for the verify step.
  otp_code_hash           bytea,
  otp_sent_to             text,                           -- phone the OTP went to (audit; never re-derived)
  otp_expires_at          timestamptz,
  otp_attempts            int NOT NULL DEFAULT 0,
  otp_verified_at         timestamptz
);

CREATE INDEX IF NOT EXISTS guarantor_consent_tokens_guarantee_idx
  ON guarantor_consent_tokens (guarantee_id, attempt_number DESC);
CREATE INDEX IF NOT EXISTS guarantor_consent_tokens_pending_idx
  ON guarantor_consent_tokens (tenant_id, expires_at)
  WHERE decision IS NULL AND used_at IS NULL;
CREATE INDEX IF NOT EXISTS guarantor_consent_tokens_reminder_idx
  ON guarantor_consent_tokens (created_at)
  WHERE decision IS NULL AND used_at IS NULL;

ALTER TABLE guarantor_consent_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE guarantor_consent_tokens FORCE ROW LEVEL SECURITY;

-- Standard tenant-isolation policy. The public endpoints bypass it
-- ONCE — for the initial token→tenant lookup — via the SECURITY
-- DEFINER function below. After that lookup the handler sets
-- app.tenant_id to the row's tenant and every subsequent query
-- runs under normal tenant scope.
CREATE POLICY tenant_isolation_guarantor_consent_tokens ON guarantor_consent_tokens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON guarantor_consent_tokens TO nexus_app;

-- find_guarantor_token_tenant — SECURITY DEFINER lookup that returns
-- (token_id, tenant_id) for a given token hash, bypassing RLS so the
-- public consent endpoint can discover the tenant before setting
-- app.tenant_id. The function only EXPOSES the tenant_id; it never
-- returns row contents. The plaintext token guard means an attacker
-- needs the 32-byte secret to enumerate.
CREATE OR REPLACE FUNCTION find_guarantor_token_tenant(p_hash bytea)
RETURNS TABLE(token_id uuid, tenant_id uuid)
LANGUAGE sql SECURITY DEFINER
SET search_path = public
AS $fn$
  SELECT id, tenant_id
    FROM guarantor_consent_tokens
   WHERE token_hash = p_hash
   LIMIT 1
$fn$;

REVOKE ALL ON FUNCTION find_guarantor_token_tenant(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_guarantor_token_tenant(bytea) TO nexus_app;


-- ─────────── tenant_operations extensions ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS guarantor_sms_enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS guarantor_sms_template text NOT NULL DEFAULT
    'Hi {{guarantor_name}}. {{applicant_name}} has requested you to guarantee a {{product_name}} of KES {{amount}}. To respond: {{link}} . Or reply to consent in person at any {{tenant_name}} branch. Valid {{expiry_days}} days. Ref: {{token_short}}',
  ADD COLUMN IF NOT EXISTS guarantor_token_expiry_days int NOT NULL DEFAULT 7,
  ADD COLUMN IF NOT EXISTS guarantor_reminder_hours_first int NOT NULL DEFAULT 48,
  ADD COLUMN IF NOT EXISTS guarantor_reminder_hours_second int NOT NULL DEFAULT 144,
  ADD COLUMN IF NOT EXISTS guarantor_max_otp_attempts int NOT NULL DEFAULT 3,
  ADD COLUMN IF NOT EXISTS guarantor_public_base_url text NOT NULL DEFAULT 'http://localhost:5173';

COMMENT ON COLUMN tenant_operations.guarantor_public_base_url IS
  'Base URL the SMS link is built from. Default is dev; prod tenants set this to their branded subdomain (e.g. https://acme.nexussacco.local).';
