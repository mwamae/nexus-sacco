-- ═══════════════════════════════════════════════════════════════════
-- Stage 2 — SMTP email channel + retry strategy.
--
-- Adds:
--   • notification_smtp_configs    — per-tenant SMTP settings; password
--     is AES-GCM-encrypted at rest (column suffix _enc by convention).
--   • next_retry_at                — when a failed delivery becomes
--     eligible for the next attempt.
--   • max_attempts                 — per-delivery cap; defaults to 4
--     (the initial attempt + 3 retries per the spec).
--   • Seeded email templates       — channel='email' rows for every
--     event in the catalog whose default_channels includes 'email'.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS notification_smtp_configs (
  tenant_id    uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  host         text NOT NULL,
  port         int  NOT NULL DEFAULT 587,
  username     text NOT NULL DEFAULT '',
  -- AES-GCM ciphertext, base64-encoded. NEVER plaintext.
  password_enc text NOT NULL DEFAULT '',
  -- 'none' | 'starttls' | 'tls' (implicit TLS aka SMTPS on :465).
  encryption   text NOT NULL DEFAULT 'starttls'
                 CHECK (encryption IN ('none', 'starttls', 'tls')),
  from_address text NOT NULL,
  from_name    text NOT NULL DEFAULT '',
  reply_to     text,
  is_active    boolean NOT NULL DEFAULT true,
  updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE notification_smtp_configs ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_smtp_configs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_smtp ON notification_smtp_configs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON notification_smtp_configs TO nexus_app;

-- Retry bookkeeping on the delivery row.
ALTER TABLE notification_deliveries
  ADD COLUMN IF NOT EXISTS next_retry_at timestamptz,
  ADD COLUMN IF NOT EXISTS max_attempts  int NOT NULL DEFAULT 4;

-- Worker scan index: pull due rows by (channel, status, next_retry_at).
CREATE INDEX IF NOT EXISTS notification_deliveries_due_idx
  ON notification_deliveries (channel, status, next_retry_at)
  WHERE status IN ('queued', 'pending');

-- Seed a dev-friendly Mailpit default for every existing tenant.
-- Production tenants will overwrite this via the admin UI.
INSERT INTO notification_smtp_configs (tenant_id, host, port, encryption, from_address, from_name)
SELECT id, 'localhost', 1025, 'none',
       lower(slug) || '@nexussacco.local',
       name || ' (Dev)'
FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- Seed email templates for every event with 'email' in default_channels.
-- Bodies are intentionally simple plain-text wrapped at render time
-- in a minimal HTML shell by the email processor. Stage 8 ships the
-- template manager where admins can replace these with branded HTML.

INSERT INTO notification_templates (tenant_id, event_code, channel, subject, body)
SELECT t.id, b.code, 'email', b.subject, b.body
FROM tenants t
CROSS JOIN (VALUES
  ('MEMBER_REGISTERED',
   'Welcome to {{tenant_name}}',
   E'Hi {{recipient_name}},\n\nWelcome — your member number is {{member_no}}.\n\nWe''re excited to have you join us.'),
  ('KYC_APPROVED',
   'KYC approved',
   E'Hi {{recipient_name}},\n\nYour KYC has been approved. You can now access all member services.'),
  ('KYC_REJECTED',
   'KYC could not be approved',
   E'Hi {{recipient_name}},\n\nWe couldn''t approve your KYC documents:\n\n  {{reason}}\n\nPlease update your details from your member portal.'),
  ('LOAN_APPLICATION_RECEIVED',
   'Loan application received',
   E'Hi {{recipient_name}},\n\nWe''ve received your loan application {{application_no}} for KES {{requested_amount}} over {{term_months}} months. We''ll be in touch with a decision shortly.'),
  ('LOAN_APPROVED',
   'Loan {{application_no}} approved',
   E'Hi {{recipient_name}},\n\nGood news — your loan application {{application_no}} has been approved:\n\n  Amount: KES {{approved_amount}}\n  Term:   {{term_months}} months\n  Rate:   {{interest_rate}}% p.a.\n\nWatch out for the offer letter shortly.'),
  ('LOAN_DECLINED',
   'Loan {{application_no}} declined',
   E'Hi {{recipient_name}},\n\nWe regret to inform you that your loan application {{application_no}} was not approved.\n\nReason: {{reason}}'),
  ('LOAN_DISBURSED',
   'Loan {{loan_no}} disbursed',
   E'Hi {{recipient_name}},\n\nYour loan {{loan_no}} has been disbursed.\n\n  Net to your account: KES {{net_disbursed}}\n  Loan principal:     KES {{principal}}'),
  ('LOAN_INSTALLMENT_DUE',
   'Loan {{loan_no}} installment due',
   E'Hi {{recipient_name}},\n\nA reminder that your loan {{loan_no}} installment of KES {{amount}} is due on {{due_date}}.'),
  ('LOAN_IN_ARREARS',
   'Loan {{loan_no}} in arrears',
   E'Hi {{recipient_name}},\n\nYour loan {{loan_no}} is {{dpd}} days past due. Outstanding balance: KES {{outstanding}}.\n\nPlease arrange repayment as soon as possible to avoid further penalties.'),
  ('LOAN_DEFAULTED',
   'Loan {{loan_no}} default',
   E'Hi {{recipient_name}},\n\nLoan {{loan_no}} has been classified as defaulted. Outstanding balance: KES {{outstanding}}.\n\nPlease contact us urgently.'),
  ('LOAN_RESTRUCTURED',
   'Loan {{loan_no}} restructured',
   E'Hi {{recipient_name}},\n\nLoan {{loan_no}} has been restructured ({{kind}}).\n\nReason: {{reason}}'),
  ('LOAN_SETTLED',
   'Loan {{loan_no}} settled',
   E'Hi {{recipient_name}},\n\nLoan {{loan_no}} has been settled in full. Thank you for your business!'),
  ('LOAN_OFFER_GENERATED',
   'Loan offer — {{application_no}}',
   E'Hi {{recipient_name}},\n\nYour loan offer for application {{application_no}} is ready:\n\n  Amount: KES {{approved_amount}}\n  Term:   {{term_months}} months\n\nPlease review and accept or decline from your portal.'),
  ('GUARANTOR_REQUEST_SENT',
   'Guarantor request',
   E'Hi {{recipient_name}},\n\n{{borrower_name}} has requested you as a guarantor on application {{application_no}} for KES {{amount}}.\n\nPlease respond from your member portal.'),
  ('GUARANTOR_REQUEST_ACCEPTED',
   'Guarantor accepted on {{application_no}}',
   E'{{guarantor_name}} accepted your guarantor request on application {{application_no}}.'),
  ('GUARANTOR_REQUEST_DECLINED',
   'Guarantor declined on {{application_no}}',
   E'{{guarantor_name}} declined your guarantor request on application {{application_no}}.\n\nReason: {{reason}}'),
  ('WITHDRAWAL_REJECTED',
   'Withdrawal rejected',
   E'Hi {{recipient_name}},\n\nA withdrawal of KES {{amount}} from {{account_no}} was rejected.\n\nReason: {{reason}}'),
  ('SHARE_PURCHASE_CONFIRMED',
   'Share purchase confirmed',
   E'Hi {{recipient_name}},\n\nYou purchased {{shares}} shares (KES {{total_value}}).'),
  ('SHARE_TRANSFER_COMPLETED',
   'Share transfer completed',
   E'Hi {{recipient_name}},\n\n{{shares}} shares have been transferred to {{to_member_name}}.'),
  ('SHARE_CERTIFICATE_ISSUED',
   'Share certificate {{certificate_no}}',
   E'Hi {{recipient_name}},\n\nA new share certificate has been issued.\n\n  Certificate: {{certificate_no}}\n  Shares held: {{shares_held}}'),
  ('INTEREST_POSTED',
   'Interest credited for {{period}}',
   E'Hi {{recipient_name}},\n\nInterest has been credited for {{period}}:\n\n  Gross interest: KES {{gross_interest}}\n  WHT:           KES {{wht}}\n  Net credited:  KES {{net_interest}}'),
  ('DIVIDEND_POSTED',
   'Dividend credited',
   E'Hi {{recipient_name}},\n\nDividend at {{rate}}% has been credited:\n\n  Gross:         KES {{gross_dividend}}\n  WHT:           KES {{wht}}\n  Net credited:  KES {{net_dividend}}'),
  ('APPROVAL_REQUEST_SENT',
   '{{kind}} awaiting approval',
   E'Hi {{recipient_name}},\n\nA {{kind}} is awaiting your approval:\n\n  {{title}}\n  Amount: KES {{amount}}'),
  ('APPROVAL_ESCALATED',
   '{{kind}} escalated: {{title}}',
   E'Hi {{recipient_name}},\n\n{{kind}} "{{title}}" has been escalated.\n\nReason: {{reason}}'),
  ('APPROVAL_SLA_BREACH',
   'SLA breach: {{title}}',
   E'Hi {{recipient_name}},\n\n{{kind}} "{{title}}" has breached its SLA — {{hours_overdue}}h overdue.'),
  ('OTP_REQUESTED',
   'Your verification code',
   E'Your verification code is {{otp}}. It will expire in {{expiry_minutes}} minutes.'),
  ('PASSWORD_RESET',
   'Password reset request',
   E'A password reset has been requested for your account.\n\nUse this link to reset (valid {{expiry_minutes}} minutes):\n\n{{reset_link}}\n\nIf you didn''t request this, please ignore this email.'),
  ('STATEMENT_GENERATED',
   'Account statement for {{period}}',
   E'Hi {{recipient_name}},\n\nYour statement for {{period}} on account {{account_no}} is attached / available in your member portal.'),
  ('DOCUMENT_EXPIRY_REMINDER',
   'Document expiring: {{document_kind}}',
   E'Hi {{recipient_name}},\n\nYour {{document_kind}} expires on {{expires_at}}. Please update it before the expiry date to avoid service interruption.'),
  ('DORMANCY_WARNING',
   'Account dormancy warning',
   E'Hi {{recipient_name}},\n\nYour account ({{member_no}}) will become dormant in {{days_until_dormant}} days due to inactivity. A single transaction will keep it active.'),
  ('ACCOUNT_DORMANT',
   'Your account is now dormant',
   E'Hi {{recipient_name}},\n\nYour account ({{member_no}}) has been marked dormant due to inactivity. Visit your branch to reactivate.')
) AS b(code, subject, body)
WHERE EXISTS (
  SELECT 1 FROM notification_events e
  WHERE e.code = b.code AND 'email' = ANY(e.default_channels)
)
ON CONFLICT (tenant_id, event_code, channel) DO NOTHING;
