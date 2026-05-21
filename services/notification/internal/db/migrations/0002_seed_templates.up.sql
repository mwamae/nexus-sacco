-- Seed in-app templates for every tenant × every event.
--
-- Stage 1 only wires LOAN_APPROVED end-to-end, but creating a default
-- template for every event means any module that fires an event later
-- gets a sensible default; an admin can customise via the template
-- manager (stage 8). Channel-specific email/SMS templates land in
-- stages 2 and 3.

INSERT INTO notification_templates (tenant_id, event_code, channel, subject, body)
SELECT t.id, b.code, 'in_app', NULL, b.body
FROM tenants t
CROSS JOIN (VALUES
  ('MEMBER_REGISTERED',          'Welcome — your member number is {{member_no}}.'),
  ('MEMBER_STATUS_CHANGED',      'Your membership status changed: {{old_status}} → {{new_status}}.'),
  ('KYC_APPROVED',               'Your KYC has been approved.'),
  ('KYC_REJECTED',               'KYC could not be approved: {{reason}}'),
  ('LOAN_APPLICATION_RECEIVED',  'Application {{application_no}} received — KES {{requested_amount}} over {{term_months}} months.'),
  ('LOAN_APPROVED',              'Loan {{application_no}} approved — KES {{approved_amount}} over {{term_months}} months at {{interest_rate}}% p.a.'),
  ('LOAN_DECLINED',              'Loan {{application_no}} was declined: {{reason}}'),
  ('LOAN_DISBURSED',             'Loan {{loan_no}} disbursed — KES {{net_disbursed}} sent to your account.'),
  ('LOAN_REPAYMENT_RECEIVED',    'Repayment of KES {{amount}} received on loan {{loan_no}}. Balance: KES {{principal_balance}}.'),
  ('LOAN_INSTALLMENT_DUE',       'Loan {{loan_no}} installment of KES {{amount}} due on {{due_date}}.'),
  ('LOAN_IN_ARREARS',            'Loan {{loan_no}} is {{dpd}} days past due. Outstanding KES {{outstanding}}.'),
  ('LOAN_DEFAULTED',             'Loan {{loan_no}} has been classified as defaulted. Outstanding KES {{outstanding}}.'),
  ('LOAN_RESTRUCTURED',          'Loan {{loan_no}} was restructured ({{kind}}): {{reason}}'),
  ('LOAN_SETTLED',               'Loan {{loan_no}} has been settled in full. Thank you!'),
  ('LOAN_WRITTEN_OFF',           'Loan {{loan_no}} (KES {{total_written_off}}) has been written off.'),
  ('LOAN_OFFER_GENERATED',       'Offer letter for application {{application_no}} is ready — KES {{approved_amount}} over {{term_months}} months.'),
  ('GUARANTOR_REQUEST_SENT',     '{{borrower_name}} requests you as a guarantor on application {{application_no}} for KES {{amount}}.'),
  ('GUARANTOR_REQUEST_ACCEPTED', '{{guarantor_name}} accepted your guarantor request on {{application_no}}.'),
  ('GUARANTOR_REQUEST_DECLINED', '{{guarantor_name}} declined your guarantor request on {{application_no}}: {{reason}}'),
  ('DEPOSIT_RECEIVED',           'KES {{amount}} deposited to {{account_no}}. New balance: KES {{new_balance}}.'),
  ('WITHDRAWAL_PROCESSED',       'KES {{amount}} withdrawn from {{account_no}}. New balance: KES {{new_balance}}.'),
  ('WITHDRAWAL_REJECTED',        'Withdrawal of KES {{amount}} from {{account_no}} was rejected: {{reason}}'),
  ('SHARE_PURCHASE_CONFIRMED',   '{{shares}} shares purchased (KES {{total_value}}).'),
  ('SHARE_TRANSFER_COMPLETED',   '{{shares}} shares transferred to {{to_member_name}}.'),
  ('SHARE_CERTIFICATE_ISSUED',   'Share certificate {{certificate_no}} issued — {{shares_held}} shares.'),
  ('INTEREST_POSTED',            'Interest credited for {{period}}: gross KES {{gross_interest}}, net KES {{net_interest}} (WHT {{wht}}).'),
  ('DIVIDEND_POSTED',            'Dividend ({{rate}}%) credited: gross KES {{gross_dividend}}, net KES {{net_dividend}} (WHT {{wht}}).'),
  ('APPROVAL_REQUEST_SENT',      '{{kind}} awaiting your approval: {{title}} (KES {{amount}}).'),
  ('APPROVAL_ACTIONED',          '{{kind}} "{{title}}" was {{status}}.'),
  ('APPROVAL_ESCALATED',         '{{kind}} "{{title}}" escalated: {{reason}}'),
  ('APPROVAL_SLA_BREACH',        '{{kind}} "{{title}}" SLA breached — {{hours_overdue}}h overdue.'),
  ('OTP_REQUESTED',              'Your code is {{otp}}. Expires in {{expiry_minutes}} minutes.'),
  ('PASSWORD_RESET',             'Reset your password: {{reset_link}} (valid {{expiry_minutes}} min).'),
  ('STATEMENT_GENERATED',        'Statement for {{period}} on {{account_no}} is ready.'),
  ('DOCUMENT_EXPIRY_REMINDER',   'Your {{document_kind}} expires on {{expires_at}}. Please update.'),
  ('DORMANCY_WARNING',           'Your account ({{member_no}}) will become dormant in {{days_until_dormant}} days.'),
  ('ACCOUNT_DORMANT',            'Your account ({{member_no}}) has been marked dormant.')
) AS b(code, body)
WHERE EXISTS (SELECT 1 FROM notification_events e WHERE e.code = b.code)
ON CONFLICT (tenant_id, event_code, channel) DO NOTHING;
