-- USER INVITES — single-use tokens for the invite-accept flow.
-- An admin invites a staff user; we create a `pending` user row with
-- no password and a token that lets them set one. Once accepted the
-- user is activated.

CREATE TABLE user_invites (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash  bytea NOT NULL UNIQUE,        -- sha256 of the raw token
  invited_by  uuid REFERENCES users(id),
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_invites_user_idx ON user_invites (user_id);

-- Pending users have no password until they accept the invite.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

ALTER TABLE user_invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_invites FORCE ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_user_invites ON user_invites
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON user_invites TO nexus_app;
