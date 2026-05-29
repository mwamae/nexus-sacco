-- Seed the GUARANTOR_CONSENT_REQUEST event + a passthrough SMS
-- template so savings can render the body server-side (using its
-- per-tenant tenant_operations.guarantor_sms_template setting) and
-- relay through the notification service for delivery.
--
-- The template body is literally `{{body}}` — the savings handler
-- passes the rendered string in payload.body and the notification
-- service substitutes it back out. This keeps tenant_operations as
-- the single source of truth for the SMS copy.

INSERT INTO notification_events (
  code, category, default_priority, description,
  default_channels, allowed_variables, is_active
) VALUES (
  'GUARANTOR_CONSENT_REQUEST', 'transactional', 'info',
  'SMS sent to a guarantor when they are added to a loan application, with a tokenised link to consent online.',
  ARRAY['sms']::notification_channel[],
  ARRAY['body', 'guarantor_name', 'applicant_name', 'amount', 'link', 'tenant_name'],
  true
)
ON CONFLICT (code) DO NOTHING;

INSERT INTO notification_templates (tenant_id, event_code, channel, subject, body)
SELECT t.id, 'GUARANTOR_CONSENT_REQUEST', 'sms', NULL, '{{body}}'
  FROM tenants t
ON CONFLICT DO NOTHING;
