-- Rollback: restore the unconditional UNIQUE constraint. Note: if any
-- (tenant, event, channel) row has multiple inactive copies, this will
-- fail — you'd need to dedupe first.

DROP INDEX IF EXISTS notification_templates_active_per_channel_idx;

ALTER TABLE notification_templates
    ADD CONSTRAINT notification_templates_tenant_id_event_code_channel_key
    UNIQUE (tenant_id, event_code, channel);
