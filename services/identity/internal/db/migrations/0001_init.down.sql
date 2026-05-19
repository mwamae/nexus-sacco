DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS password_resets;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS tenants;

DROP TYPE IF EXISTS user_status;
DROP TYPE IF EXISTS tenant_status;
DROP TYPE IF EXISTS tenant_kind;

DROP FUNCTION IF EXISTS current_tenant_id();
DROP FUNCTION IF EXISTS set_updated_at();
