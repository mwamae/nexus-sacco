-- Rollback recreates the per-tenant SMTP + SMS config tables in the
-- shape they had immediately before migration 0011. Re-applying 0003
-- and 0004 down + up would be the alternative, but this script keeps
-- the rollback path self-contained.

CREATE TABLE IF NOT EXISTS notification_smtp_configs (
    tenant_id       uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    host            text NOT NULL DEFAULT '',
    port            integer NOT NULL DEFAULT 587,
    username        text NOT NULL DEFAULT '',
    password_enc    text NOT NULL DEFAULT '',
    encryption      text NOT NULL DEFAULT 'starttls',
    from_address    text NOT NULL DEFAULT '',
    from_name       text NOT NULL DEFAULT '',
    reply_to        text,
    is_active       boolean NOT NULL DEFAULT true,
    updated_at      timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE notification_smtp_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_smtp_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_smtp_cfg ON notification_smtp_configs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_smtp_configs TO nexus_app;

CREATE TABLE IF NOT EXISTS notification_sms_configs (
    tenant_id          uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
    provider           text NOT NULL DEFAULT 'mock' CHECK (provider IN ('mock','sandbox','production')),
    username           text NOT NULL DEFAULT '',
    api_key_enc        text NOT NULL DEFAULT '',
    sender_id          text NOT NULL DEFAULT '',
    rate_per_minute    integer NOT NULL DEFAULT 600,
    webhook_secret_enc text NOT NULL DEFAULT '',
    is_active          boolean NOT NULL DEFAULT true,
    updated_at         timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE notification_sms_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_sms_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_sms_cfg ON notification_sms_configs
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_sms_configs TO nexus_app;
