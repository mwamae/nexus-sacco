-- ═══════════════════════════════════════════════════════════════════
-- Notifications Module — Stage 1: foundation.
--
-- Schema is multi-tenant via RLS (current_tenant_id() GUC). Every
-- per-tenant table has tenant_id + a tenant_isolation_* policy.
--
-- Five tables:
--   • notification_events       — central registry of event codes
--                                 (tenant-agnostic; shared catalog)
--   • notification_templates    — per-tenant, per-(event×channel) body
--   • notifications             — one row per "send" across all channels
--   • notification_deliveries   — one row per channel attempt
--   • notification_preferences  — per-member channel opt-outs
-- ═══════════════════════════════════════════════════════════════════

CREATE TYPE notification_category   AS ENUM ('transactional', 'campaign', 'system');
CREATE TYPE notification_priority   AS ENUM ('info', 'success', 'warning', 'error');
CREATE TYPE notification_channel    AS ENUM ('in_app', 'sms', 'email');
CREATE TYPE notification_status     AS ENUM (
  'pending', 'queued', 'sent', 'delivered', 'read', 'failed'
);

-- ─────────── Event registry ───────────
--
-- Tenant-agnostic catalog. Defines what events exist, their default
-- priority, the set of variables their templates may reference, and
-- which channels they default to when a notification is fired without
-- an explicit channel list.

CREATE TABLE notification_events (
  code               text PRIMARY KEY,
  category           notification_category NOT NULL DEFAULT 'transactional',
  default_priority   notification_priority NOT NULL DEFAULT 'info',
  description        text NOT NULL,
  -- Default fan-out channels when the caller doesn't override.
  default_channels   notification_channel[] NOT NULL DEFAULT ARRAY['in_app']::notification_channel[],
  -- Declared variable allow-list — used for template validation in the
  -- admin UI; not enforced at render time.
  allowed_variables  text[] NOT NULL DEFAULT ARRAY[]::text[],
  has_pdf_attachment boolean NOT NULL DEFAULT false,
  is_active          boolean NOT NULL DEFAULT true,
  created_at         timestamptz NOT NULL DEFAULT now()
);

-- ─────────── Templates (per-tenant) ───────────

CREATE TABLE notification_templates (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_code  text NOT NULL REFERENCES notification_events(code) ON DELETE RESTRICT,
  channel     notification_channel NOT NULL,
  -- email subject (NULL for sms / in_app)
  subject     text,
  body        text NOT NULL,
  is_active   boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, event_code, channel)
);

CREATE INDEX notification_templates_event_idx
  ON notification_templates (tenant_id, event_code);

-- ─────────── Notifications (one per send-event) ───────────

CREATE TABLE notifications (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_code          text NOT NULL,
  priority            notification_priority NOT NULL DEFAULT 'info',

  -- Recipient: at least one of member_id / user_id is set.
  recipient_member_id uuid,
  recipient_user_id   uuid,
  recipient_name      text NOT NULL DEFAULT '',
  recipient_phone     text,
  recipient_email     text,

  -- Provenance — what triggered this notification?
  source_module       text,    -- e.g. 'savings.loans'
  source_record_id    uuid,    -- e.g. loan id
  deep_link           text,    -- in-app navigation target, e.g. '/loans/<id>'

  -- Variables for template rendering (snapshot at send time).
  payload             jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- Who initiated. NULL when triggered by a scheduled / system event.
  initiated_by        uuid,
  created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notifications_recipient_user_idx
  ON notifications (tenant_id, recipient_user_id, created_at DESC)
  WHERE recipient_user_id IS NOT NULL;

CREATE INDEX notifications_recipient_member_idx
  ON notifications (tenant_id, recipient_member_id, created_at DESC)
  WHERE recipient_member_id IS NOT NULL;

CREATE INDEX notifications_source_idx
  ON notifications (tenant_id, source_module, source_record_id);

CREATE INDEX notifications_event_idx
  ON notifications (tenant_id, event_code, created_at DESC);

-- ─────────── Deliveries (per-channel attempts) ───────────

CREATE TABLE notification_deliveries (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  notification_id     uuid NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
  channel             notification_channel NOT NULL,
  template_id         uuid REFERENCES notification_templates(id) ON DELETE SET NULL,

  -- Rendered content snapshot.
  subject             text,
  body                text NOT NULL,

  status              notification_status NOT NULL DEFAULT 'pending',
  attempt_count       int NOT NULL DEFAULT 0,

  queued_at           timestamptz,
  sent_at             timestamptz,
  delivered_at        timestamptz,
  read_at             timestamptz,
  failed_at           timestamptz,
  failure_reason      text,

  -- Provider-side correlation id (e.g. AT message id, SMTP message-id).
  provider_message_id text,

  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_deliveries_notif_idx
  ON notification_deliveries (notification_id);

CREATE INDEX notification_deliveries_status_idx
  ON notification_deliveries (tenant_id, channel, status, created_at);

-- ─────────── Per-member preferences ───────────

CREATE TABLE notification_preferences (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_id             uuid NOT NULL,
  allow_campaign_sms    boolean NOT NULL DEFAULT true,
  allow_campaign_email  boolean NOT NULL DEFAULT true,
  allow_campaign_in_app boolean NOT NULL DEFAULT true,
  preferred_language    text NOT NULL DEFAULT 'en',
  updated_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, member_id)
);

-- ─────────── RLS + grants ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'notification_templates', 'notifications',
    'notification_deliveries', 'notification_preferences'
  ])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON
  notification_events, notification_templates,
  notifications, notification_deliveries, notification_preferences
TO nexus_app;

-- ─────────── Seed event catalog ───────────
--
-- Stage 1 ships with one wired event (LOAN_APPROVED) and registers the
-- full event catalog so subsequent stages can attach templates without
-- another migration. has_pdf_attachment marks the ones that will
-- trigger PDF generation in stage 5.

INSERT INTO notification_events (code, category, default_priority, description, default_channels, allowed_variables, has_pdf_attachment) VALUES
  -- Members / KYC
  ('MEMBER_REGISTERED',           'transactional', 'success', 'New member onboarded',                                 ARRAY['in_app','email']::notification_channel[], ARRAY['member_no','full_name'],                                                    false),
  ('MEMBER_STATUS_CHANGED',       'transactional', 'info',    'Member status changed',                                ARRAY['in_app']::notification_channel[],         ARRAY['member_no','full_name','old_status','new_status'],                          false),
  ('KYC_APPROVED',                'transactional', 'success', 'KYC approved',                                          ARRAY['in_app','email']::notification_channel[], ARRAY['member_no','full_name'],                                                    false),
  ('KYC_REJECTED',                'transactional', 'warning', 'KYC rejected',                                          ARRAY['in_app','email']::notification_channel[], ARRAY['member_no','full_name','reason'],                                           false),
  -- Loans
  ('LOAN_APPLICATION_RECEIVED',   'transactional', 'info',    'Loan application submitted',                            ARRAY['in_app','email']::notification_channel[], ARRAY['application_no','requested_amount','term_months'],                          false),
  ('LOAN_APPROVED',               'transactional', 'success', 'Loan approved',                                         ARRAY['in_app','email']::notification_channel[], ARRAY['application_no','loan_no','approved_amount','term_months','interest_rate'], false),
  ('LOAN_DECLINED',               'transactional', 'warning', 'Loan declined',                                         ARRAY['in_app','email']::notification_channel[], ARRAY['application_no','reason'],                                                  false),
  ('LOAN_DISBURSED',              'transactional', 'success', 'Loan disbursed',                                        ARRAY['in_app','email','sms']::notification_channel[], ARRAY['loan_no','net_disbursed','principal'],                               true),
  ('LOAN_REPAYMENT_RECEIVED',     'transactional', 'success', 'Loan repayment received',                               ARRAY['in_app','sms']::notification_channel[],   ARRAY['loan_no','amount','principal_balance'],                                     false),
  ('LOAN_INSTALLMENT_DUE',        'transactional', 'info',    'Loan installment due',                                  ARRAY['in_app','sms','email']::notification_channel[], ARRAY['loan_no','amount','due_date'],                                      false),
  ('LOAN_IN_ARREARS',             'transactional', 'warning', 'Loan in arrears',                                       ARRAY['in_app','sms','email']::notification_channel[], ARRAY['loan_no','dpd','outstanding'],                                       false),
  ('LOAN_DEFAULTED',              'transactional', 'error',   'Loan defaulted',                                        ARRAY['in_app','email']::notification_channel[], ARRAY['loan_no','outstanding'],                                                    false),
  ('LOAN_RESTRUCTURED',           'transactional', 'info',    'Loan restructured',                                     ARRAY['in_app','email']::notification_channel[], ARRAY['loan_no','kind','reason'],                                                  true),
  ('LOAN_SETTLED',                'transactional', 'success', 'Loan settled',                                          ARRAY['in_app','email']::notification_channel[], ARRAY['loan_no'],                                                                  true),
  ('LOAN_WRITTEN_OFF',            'transactional', 'error',   'Loan written off',                                      ARRAY['in_app']::notification_channel[],         ARRAY['loan_no','total_written_off'],                                              false),
  ('LOAN_OFFER_GENERATED',        'transactional', 'info',    'Loan offer letter generated',                           ARRAY['in_app','email']::notification_channel[], ARRAY['application_no','approved_amount','term_months'],                           true),
  -- Guarantees
  ('GUARANTOR_REQUEST_SENT',      'transactional', 'info',    'Guarantor request sent',                                ARRAY['in_app','sms','email']::notification_channel[], ARRAY['borrower_name','amount','application_no'],                          false),
  ('GUARANTOR_REQUEST_ACCEPTED',  'transactional', 'success', 'Guarantor accepted',                                    ARRAY['in_app','email']::notification_channel[], ARRAY['guarantor_name','application_no'],                                          false),
  ('GUARANTOR_REQUEST_DECLINED',  'transactional', 'warning', 'Guarantor declined',                                    ARRAY['in_app','email']::notification_channel[], ARRAY['guarantor_name','application_no','reason'],                                 false),
  -- Deposits / withdrawals
  ('DEPOSIT_RECEIVED',            'transactional', 'success', 'Deposit posted',                                        ARRAY['in_app','sms']::notification_channel[],   ARRAY['account_no','amount','new_balance'],                                        false),
  ('WITHDRAWAL_PROCESSED',        'transactional', 'success', 'Withdrawal processed',                                  ARRAY['in_app','sms']::notification_channel[],   ARRAY['account_no','amount','new_balance'],                                        false),
  ('WITHDRAWAL_REJECTED',         'transactional', 'warning', 'Withdrawal rejected',                                   ARRAY['in_app','sms','email']::notification_channel[], ARRAY['account_no','amount','reason'],                                      false),
  -- Shares / interest / dividends
  ('SHARE_PURCHASE_CONFIRMED',    'transactional', 'success', 'Share purchase confirmed',                              ARRAY['in_app','email']::notification_channel[], ARRAY['shares','total_value'],                                                     false),
  ('SHARE_TRANSFER_COMPLETED',    'transactional', 'success', 'Share transfer completed',                              ARRAY['in_app','email']::notification_channel[], ARRAY['shares','to_member_name'],                                                  false),
  ('SHARE_CERTIFICATE_ISSUED',    'transactional', 'success', 'Share certificate issued',                              ARRAY['in_app','email']::notification_channel[], ARRAY['certificate_no','shares_held'],                                             true),
  ('INTEREST_POSTED',             'transactional', 'success', 'Interest posted',                                       ARRAY['in_app','email']::notification_channel[], ARRAY['gross_interest','wht','net_interest','period'],                             true),
  ('DIVIDEND_POSTED',             'transactional', 'success', 'Dividend posted',                                       ARRAY['in_app','email']::notification_channel[], ARRAY['gross_dividend','wht','net_dividend','rate'],                               true),
  -- Approvals / workflow
  ('APPROVAL_REQUEST_SENT',       'transactional', 'info',    'Approval request awaiting action',                      ARRAY['in_app','email']::notification_channel[], ARRAY['kind','title','amount'],                                                    false),
  ('APPROVAL_ACTIONED',           'transactional', 'success', 'Approval decision made',                                ARRAY['in_app']::notification_channel[],         ARRAY['kind','status','title'],                                                    false),
  ('APPROVAL_ESCALATED',          'transactional', 'warning', 'Approval escalated',                                    ARRAY['in_app','email']::notification_channel[], ARRAY['kind','title','reason'],                                                    false),
  ('APPROVAL_SLA_BREACH',         'system',        'error',   'Approval SLA breach',                                   ARRAY['in_app','email']::notification_channel[], ARRAY['kind','title','hours_overdue'],                                             false),
  -- Auth
  ('OTP_REQUESTED',               'transactional', 'info',    'OTP code',                                              ARRAY['sms','email']::notification_channel[],    ARRAY['otp','expiry_minutes'],                                                     false),
  ('PASSWORD_RESET',              'transactional', 'info',    'Password reset link',                                   ARRAY['email']::notification_channel[],          ARRAY['reset_link','expiry_minutes'],                                              false),
  -- Documents
  ('STATEMENT_GENERATED',         'transactional', 'info',    'Account statement available',                           ARRAY['email','in_app']::notification_channel[], ARRAY['period','account_no'],                                                      true),
  ('DOCUMENT_EXPIRY_REMINDER',    'transactional', 'warning', 'Document expiring soon',                                ARRAY['in_app','sms','email']::notification_channel[], ARRAY['document_kind','expires_at'],                                       false),
  -- Lifecycle
  ('DORMANCY_WARNING',            'transactional', 'warning', 'Account approaching dormancy',                          ARRAY['in_app','email']::notification_channel[], ARRAY['member_no','days_until_dormant'],                                           false),
  ('ACCOUNT_DORMANT',             'transactional', 'warning', 'Account marked dormant',                                ARRAY['in_app','email']::notification_channel[], ARRAY['member_no'],                                                                false)
ON CONFLICT (code) DO NOTHING;
