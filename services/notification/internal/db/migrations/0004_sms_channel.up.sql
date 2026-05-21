-- ═══════════════════════════════════════════════════════════════════
-- Stage 3 — SMS channel via Africa's Talking.
--
-- Adds:
--   • notification_sms_configs    — per-tenant AT credentials + sender id.
--     api_key_enc and webhook_secret_enc are AES-GCM-encrypted.
--     provider supports a 'mock' value so dev/test environments work
--     without an AT account; 'sandbox' hits AT's sandbox endpoint;
--     'production' hits the live endpoint.
--   • Seeded SMS templates for every event whose default_channels
--     includes 'sms'. Bodies are intentionally short (≤160 chars
--     where possible — long ones still send but bill as concatenated).
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS notification_sms_configs (
  tenant_id          uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  provider           text NOT NULL DEFAULT 'mock'
                       CHECK (provider IN ('mock', 'sandbox', 'production')),
  username           text NOT NULL DEFAULT '',
  api_key_enc        text NOT NULL DEFAULT '',
  sender_id          text NOT NULL DEFAULT '',
  -- Throttle: hard cap on SMS dispatched per minute per tenant. The
  -- worker claims at most ceil(rate_per_minute / 6) rows each 10s tick.
  rate_per_minute    int NOT NULL DEFAULT 600,
  -- Optional shared secret for the delivery-report webhook. When set,
  -- the webhook handler validates a HMAC header before applying the
  -- status update. AT itself doesn't sign, but operators may put a
  -- reverse proxy in front that does.
  webhook_secret_enc text NOT NULL DEFAULT '',
  is_active          boolean NOT NULL DEFAULT true,
  updated_at         timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE notification_sms_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_sms_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_sms_cfg ON notification_sms_configs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON notification_sms_configs TO nexus_app;

-- Seed mock default per tenant so the worker has something to read.
INSERT INTO notification_sms_configs (tenant_id, provider, sender_id)
SELECT id, 'mock', upper(substring(slug FROM 1 FOR 11))
FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- ─────────── SMS templates ───────────
--
-- Keep these < 160 chars where the event has the data to support it.
-- Tenant admins can override per template via the manager (Stage 8).

INSERT INTO notification_templates (tenant_id, event_code, channel, subject, body)
SELECT t.id, b.code, 'sms', NULL, b.body
FROM tenants t
CROSS JOIN (VALUES
  ('LOAN_APPROVED',
   'Your loan {{application_no}} for KES {{approved_amount}} ({{term_months}}mo @ {{interest_rate}}%) has been approved. Watch out for the offer letter.'),
  ('LOAN_DISBURSED',
   'Loan {{loan_no}}: KES {{net_disbursed}} credited to your account.'),
  ('LOAN_REPAYMENT_RECEIVED',
   'KES {{amount}} repayment received on loan {{loan_no}}. Outstanding KES {{principal_balance}}. Thank you.'),
  ('LOAN_INSTALLMENT_DUE',
   'REMINDER: loan {{loan_no}} installment of KES {{amount}} is due on {{due_date}}.'),
  ('LOAN_IN_ARREARS',
   'Loan {{loan_no}} is {{dpd}} days past due. Outstanding KES {{outstanding}}. Please pay to avoid further charges.'),
  ('GUARANTOR_REQUEST_SENT',
   '{{borrower_name}} has requested you as guarantor on a KES {{amount}} loan ({{application_no}}). Open the app to accept or decline.'),
  ('DEPOSIT_RECEIVED',
   'KES {{amount}} deposited to {{account_no}}. New balance KES {{new_balance}}.'),
  ('WITHDRAWAL_PROCESSED',
   'KES {{amount}} withdrawn from {{account_no}}. New balance KES {{new_balance}}.'),
  ('WITHDRAWAL_REJECTED',
   'Withdrawal of KES {{amount}} from {{account_no}} was rejected: {{reason}}'),
  ('OTP_REQUESTED',
   'Your verification code is {{otp}}. Expires in {{expiry_minutes}} min. Do not share.'),
  ('DOCUMENT_EXPIRY_REMINDER',
   'Your {{document_kind}} expires on {{expires_at}}. Please update it to keep your account active.')
) AS b(code, body)
WHERE EXISTS (
  SELECT 1 FROM notification_events e
  WHERE e.code = b.code AND 'sms' = ANY(e.default_channels)
)
ON CONFLICT (tenant_id, event_code, channel) DO NOTHING;

-- ─────────── Worker scan index also covers SMS ───────────
-- (the partial WHERE in 0003 already includes 'queued' rows of any
--  channel, so no additional index needed)
