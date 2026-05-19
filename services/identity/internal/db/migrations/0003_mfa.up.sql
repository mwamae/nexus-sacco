-- MFA: email-based one-time codes.
--
-- mfa_challenges holds short-lived challenges issued by login (purpose='login')
-- or by the enable-MFA flow (purpose='enable_mfa'). The raw mfa_token and the
-- raw code are returned to the client / sent via email respectively; we only
-- store SHA-256 hashes server-side. Codes are 6 digits; the mfa_token is
-- 32 random URL-safe bytes.
--
-- A challenge is consumed (used_at set) on successful verify; replay → 401.
-- Attempts is bumped on each wrong code; locked after 5 wrong submissions.

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS mfa_method text;
COMMENT ON COLUMN users.mfa_method IS 'email | totp (totp not yet implemented)';

CREATE TABLE mfa_challenges (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose         text NOT NULL CHECK (purpose IN ('login', 'enable_mfa')),
  mfa_token_hash  bytea NOT NULL UNIQUE,
  code_hash       bytea NOT NULL,
  expires_at      timestamptz NOT NULL,
  used_at         timestamptz,
  attempts        int NOT NULL DEFAULT 0,
  ip              inet,
  user_agent      text,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_user_idx ON mfa_challenges (user_id, created_at DESC);
CREATE INDEX mfa_challenges_expires_idx ON mfa_challenges (expires_at) WHERE used_at IS NULL;

ALTER TABLE mfa_challenges ENABLE ROW LEVEL SECURITY;
ALTER TABLE mfa_challenges FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_mfa_challenges ON mfa_challenges
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON mfa_challenges TO nexus_app;
