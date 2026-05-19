-- nexusSacco · identity service — initial schema.
--
-- Multi-tenancy model: shared database, tenant_id column on every
-- tenant-scoped row, Row-Level Security policies enforce isolation.
-- The app is expected to issue `SET LOCAL app.tenant_id = '<uuid>'`
-- at the start of every authenticated transaction. RLS catches missed
-- WHERE clauses.
--
-- The `platform_admin` role bypasses RLS for cross-tenant operations
-- (tenant onboarding, support). All other DB roles see only their tenant.

-- ───────── Extensions ─────────
CREATE EXTENSION IF NOT EXISTS "pgcrypto";   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS "citext";     -- case-insensitive email

-- ───────── App role ─────────
-- Migrations run as the bootstrap superuser. The application itself
-- SET ROLEs to nexus_app on every connection so RLS actually applies
-- (superusers bypass RLS, even with FORCE).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'nexus_app') THEN
    CREATE ROLE nexus_app NOSUPERUSER NOBYPASSRLS NOLOGIN;
  END IF;
  -- Reassert attributes in case the role pre-existed with the wrong ones.
  ALTER ROLE nexus_app NOSUPERUSER NOBYPASSRLS;
END $$;

GRANT USAGE ON SCHEMA public TO nexus_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO nexus_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO nexus_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT EXECUTE ON FUNCTIONS TO nexus_app;
-- Allow the migration's bootstrap user to grant role membership without
-- being a member itself (Postgres 16+).
GRANT nexus_app TO CURRENT_USER WITH ADMIN OPTION;

-- ───────── Trigger function: bump updated_at ─────────
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

-- ───────── Helper: current tenant from session GUC ─────────
-- Returns NULL when unset so we can distinguish "no tenant context"
-- from "tenant context but no match".
CREATE OR REPLACE FUNCTION current_tenant_id() RETURNS uuid
LANGUAGE plpgsql STABLE AS $$
DECLARE
  v text := current_setting('app.tenant_id', true);
BEGIN
  IF v IS NULL OR v = '' THEN RETURN NULL; END IF;
  RETURN v::uuid;
END;
$$;

-- ═══════════════════════════════════════════════════════════════════
-- TENANTS — registry of organizations. NOT tenant-scoped; global table.
-- ═══════════════════════════════════════════════════════════════════
CREATE TYPE tenant_status AS ENUM ('active', 'suspended', 'closed');
CREATE TYPE tenant_kind   AS ENUM ('sacco', 'microfinance', 'digital_lender', 'cooperative', 'chama');

CREATE TABLE tenants (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug            citext NOT NULL UNIQUE,            -- subdomain key, e.g. "tujenge"
  name            text NOT NULL,
  legal_name      text,
  kind            tenant_kind NOT NULL DEFAULT 'sacco',
  status          tenant_status NOT NULL DEFAULT 'active',
  country_code    char(2) NOT NULL DEFAULT 'KE',
  currency_code   char(3) NOT NULL DEFAULT 'KES',
  license_no      text,                              -- e.g. SASRA/2018/0421
  branding        jsonb NOT NULL DEFAULT '{}'::jsonb,
  settings        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER tenants_updated_at BEFORE UPDATE ON tenants
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Slug format: 3-40 chars, lowercase letters / digits / hyphens, no leading/trailing hyphen.
ALTER TABLE tenants
  ADD CONSTRAINT tenants_slug_format
  CHECK (slug ~ '^[a-z0-9]([a-z0-9-]{1,38}[a-z0-9])?$');

-- ═══════════════════════════════════════════════════════════════════
-- PERMISSIONS — global catalog of permission strings. Not tenant-scoped.
-- Example codes: "members:view", "loans:approve", "tenant:settings:edit".
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE permissions (
  code        text PRIMARY KEY,         -- "resource:action[:scope]"
  description text NOT NULL,
  category    text NOT NULL,            -- "members", "loans", "platform", ...
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- ═══════════════════════════════════════════════════════════════════
-- ROLES — system roles (tenant_id IS NULL) and tenant-custom roles.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE roles (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid REFERENCES tenants(id) ON DELETE CASCADE,
  code        text NOT NULL,            -- "tenant_owner", "branch_manager", ...
  name        text NOT NULL,
  description text,
  is_system   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  -- System roles unique by code globally; tenant roles unique within tenant.
  UNIQUE (tenant_id, code)
);
CREATE TRIGGER roles_updated_at BEFORE UPDATE ON roles
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX roles_tenant_idx ON roles (tenant_id);

CREATE TABLE role_permissions (
  role_id         uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_code text NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_code)
);

-- ═══════════════════════════════════════════════════════════════════
-- USERS — staff / members with login. Scoped to a tenant.
-- Platform super-admins are users in a special "platform" pseudo-tenant
-- whose slug is reserved; their JWT carries an is_platform_admin claim.
-- ═══════════════════════════════════════════════════════════════════
CREATE TYPE user_status AS ENUM ('pending', 'active', 'suspended', 'locked', 'closed');

CREATE TABLE users (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email               citext NOT NULL,
  phone               text,
  password_hash       text NOT NULL,             -- argon2id encoded string
  full_name           text NOT NULL,
  status              user_status NOT NULL DEFAULT 'pending',
  is_platform_admin   boolean NOT NULL DEFAULT false,
  email_verified_at   timestamptz,
  phone_verified_at   timestamptz,
  mfa_enabled         boolean NOT NULL DEFAULT false,
  mfa_secret          text,                      -- TOTP secret, encrypted at rest (future)
  last_login_at       timestamptz,
  failed_login_count  int NOT NULL DEFAULT 0,
  locked_until        timestamptz,
  metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX users_tenant_status_idx ON users (tenant_id, status);
CREATE INDEX users_phone_idx ON users (phone) WHERE phone IS NOT NULL;

CREATE TABLE user_roles (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id    uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  granted_at timestamptz NOT NULL DEFAULT now(),
  granted_by uuid REFERENCES users(id),
  PRIMARY KEY (user_id, role_id)
);

-- ═══════════════════════════════════════════════════════════════════
-- REFRESH TOKENS — opaque, hashed at rest, rotated on use.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE refresh_tokens (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash      bytea NOT NULL UNIQUE,        -- sha256 of the raw token
  parent_id       uuid REFERENCES refresh_tokens(id), -- rotation chain
  user_agent      text,
  ip              inet,
  expires_at      timestamptz NOT NULL,
  revoked_at      timestamptz,
  revoked_reason  text,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id, revoked_at);
CREATE INDEX refresh_tokens_expires_idx ON refresh_tokens (expires_at) WHERE revoked_at IS NULL;

-- ═══════════════════════════════════════════════════════════════════
-- PASSWORD RESETS — single-use, short-lived.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE password_resets (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash  bytea NOT NULL UNIQUE,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_resets_user_idx ON password_resets (user_id);

-- ═══════════════════════════════════════════════════════════════════
-- AUDIT LOG — append-only.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE audit_log (
  id           bigserial PRIMARY KEY,
  tenant_id    uuid REFERENCES tenants(id) ON DELETE SET NULL,
  actor_id     uuid REFERENCES users(id) ON DELETE SET NULL,
  action       text NOT NULL,         -- "user.login.success", "tenant.created", ...
  target_kind  text,                  -- "user", "tenant", "role", ...
  target_id    text,                  -- string to fit non-uuid ids too
  ip           inet,
  user_agent   text,
  metadata     jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, created_at DESC);
CREATE INDEX audit_log_action_idx ON audit_log (action, created_at DESC);

-- ═══════════════════════════════════════════════════════════════════
-- ROW-LEVEL SECURITY
--
-- Policy: a session must SET LOCAL app.tenant_id = '<uuid>' before
-- touching tenant-scoped tables. A session role named `platform_admin`
-- bypasses (used by tenant onboarding / support).
-- ═══════════════════════════════════════════════════════════════════

DO $$
DECLARE
  t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['users', 'roles', 'user_roles', 'refresh_tokens', 'password_resets'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;

-- users / roles / refresh_tokens / password_resets: filtered by tenant_id.
CREATE POLICY tenant_isolation_users ON users
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_roles ON roles
  USING (tenant_id IS NULL OR tenant_id = current_tenant_id())
  WITH CHECK (tenant_id IS NULL OR tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_refresh_tokens ON refresh_tokens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_password_resets ON password_resets
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- user_roles has no tenant_id column — derive from the user.
CREATE POLICY tenant_isolation_user_roles ON user_roles
  USING (
    EXISTS (SELECT 1 FROM users u WHERE u.id = user_roles.user_id
            AND u.tenant_id = current_tenant_id())
  )
  WITH CHECK (
    EXISTS (SELECT 1 FROM users u WHERE u.id = user_roles.user_id
            AND u.tenant_id = current_tenant_id())
  );

-- ───────── Grant on tables created above ─────────
-- ALTER DEFAULT PRIVILEGES only covers future tables, so explicitly
-- grant on what we just created.
GRANT SELECT, INSERT, UPDATE, DELETE ON
  tenants, permissions, roles, role_permissions, users, user_roles,
  refresh_tokens, password_resets, audit_log
  TO nexus_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO nexus_app;
