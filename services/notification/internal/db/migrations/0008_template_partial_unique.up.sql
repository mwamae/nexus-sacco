-- Stage 8: relax the notification_templates unique constraint so
-- inactive copies can coexist with the single active one per
-- (tenant_id, event_code, channel). Lets admins keep history + stage
-- new versions via the clone-and-deactivate flow in the template
-- manager UI.

ALTER TABLE notification_templates
    DROP CONSTRAINT IF EXISTS notification_templates_tenant_id_event_code_channel_key;

CREATE UNIQUE INDEX IF NOT EXISTS notification_templates_active_per_channel_idx
    ON notification_templates (tenant_id, event_code, channel)
    WHERE is_active = true;
