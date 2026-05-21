-- ═══════════════════════════════════════════════════════════════════
-- Stage 6 — Centralized OTP / 2FA.
--
-- The notification service is now the single source of truth for OTP
-- generation, delivery, and verification across the platform. Every
-- other service (identity for MFA, savings for transaction sign-off,
-- member portal for self-service confirms) calls the internal OTP API
-- here rather than rolling its own.
--
-- Codes are HMAC-SHA256-hashed before storage with the shared crypto
-- key (same JWT_SECRET-derived key used for SMTP / AT credentials).
-- The plain code is never persisted. Verification recomputes the
-- hash and constant-time compares.
-- ═══════════════════════════════════════════════════════════════════

CREATE TYPE otp_purpose AS ENUM (
  'login_mfa',             -- second factor on staff login
  'password_reset',        -- forgot-password flow
  'transaction_verify',    -- sign-off on a large transaction
  'member_self_service',   -- member portal sensitive action
  'phone_verify',          -- phone number verification
  'email_verify',          -- email verification
  'other'                  -- catch-all for custom flows
);

CREATE TYPE otp_status AS ENUM (
  'pending',    -- code issued, not yet verified
  'verified',   -- correct code submitted
  'expired',    -- past expiry; can no longer be verified
  'exhausted',  -- attempts_used reached max_attempts
  'cancelled'   -- explicitly cancelled (rare)
);

CREATE TABLE IF NOT EXISTS otp_requests (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  purpose             otp_purpose NOT NULL,

  -- Who/what this OTP is for. At least one of subject_user_id /
  -- subject_member_id / subject_identifier should be set.
  subject_user_id     uuid,
  subject_member_id   uuid,
  subject_identifier  text,            -- email or phone if no DB id is available

  -- Delivery
  channel             notification_channel NOT NULL,
  destination         text NOT NULL,   -- phone or email actually delivered to

  -- Code — HMAC-SHA256 hex of (key, plaintext_code). NEVER plaintext.
  code_hash           text NOT NULL,
  code_length         int  NOT NULL,

  -- Lifecycle
  status              otp_status NOT NULL DEFAULT 'pending',
  attempts_used       int NOT NULL DEFAULT 0,
  max_attempts        int NOT NULL DEFAULT 3,
  generated_at        timestamptz NOT NULL DEFAULT now(),
  expires_at          timestamptz NOT NULL,
  verified_at         timestamptz,

  -- Audit
  ip_address          inet,
  device_fingerprint  text,
  notification_id     uuid REFERENCES notifications(id) ON DELETE SET NULL,
  created_by          uuid
);
CREATE INDEX IF NOT EXISTS otp_requests_subject_user_idx
  ON otp_requests (tenant_id, subject_user_id, generated_at DESC)
  WHERE subject_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS otp_requests_subject_member_idx
  ON otp_requests (tenant_id, subject_member_id, generated_at DESC)
  WHERE subject_member_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS otp_requests_purpose_status_idx
  ON otp_requests (tenant_id, purpose, status, generated_at DESC);

-- ─────────── Per-tenant policy ───────────

CREATE TABLE IF NOT EXISTS otp_settings (
  tenant_id               uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  code_length             int  NOT NULL DEFAULT 6
                            CHECK (code_length BETWEEN 4 AND 8),
  expiry_minutes          int  NOT NULL DEFAULT 5
                            CHECK (expiry_minutes BETWEEN 1 AND 60),
  max_attempts            int  NOT NULL DEFAULT 3
                            CHECK (max_attempts BETWEEN 3 AND 5),
  resend_cooldown_seconds int  NOT NULL DEFAULT 60
                            CHECK (resend_cooldown_seconds BETWEEN 15 AND 600),
  default_channel         notification_channel NOT NULL DEFAULT 'sms',
  updated_at              timestamptz NOT NULL DEFAULT now()
);

-- Seed default policy per existing tenant.
INSERT INTO otp_settings (tenant_id)
SELECT id FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- ─────────── RLS + grants ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['otp_requests', 'otp_settings'])
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

GRANT SELECT, INSERT, UPDATE, DELETE ON otp_requests, otp_settings TO nexus_app;
