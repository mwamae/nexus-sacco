-- DSID Phase 2.1 — Statements (PDF + email) + WHT remittance.
--
-- Adds:
--   1. statement_mailings — idempotency log for the statement-mailer
--      cron worker. One row per (tenant, counterparty, period_label).
--   2. member_notification_preferences — per-counterparty flags that
--      drive whether the cron mailer hits them + which channels.
--   3. tenant_operations.statement_mail_cron — schedule the mailer
--      runs at per tenant. Defaults to '0 6 1 */3 *' (6am on the 1st
--      of every 3rd month — quarterly).

BEGIN;

-- ─────────── 1. statement_mailings ───────────

CREATE TABLE statement_mailings (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id   uuid NOT NULL,
  period_label      text NOT NULL,                                         -- 'FY2025-2026', '2026-Q2', etc.
  email_address     text NOT NULL,
  statement_kinds   text[] NOT NULL,                                       -- which kinds were sent
  pdf_attachment_ids uuid[] NOT NULL DEFAULT ARRAY[]::uuid[],              -- pdf_documents row ids when known
  sent_at           timestamptz NOT NULL DEFAULT now(),
  bounce_status     text,                                                   -- NULL = sent OK; non-NULL = bounce reason
  notification_event_id uuid,                                              -- the notification_events row id for the dispatch
  UNIQUE (tenant_id, counterparty_id, period_label)
);
CREATE INDEX statement_mailings_tenant_period_idx
  ON statement_mailings (tenant_id, period_label, sent_at DESC);

ALTER TABLE statement_mailings ENABLE ROW LEVEL SECURITY;
ALTER TABLE statement_mailings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_statement_mailings ON statement_mailings
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON statement_mailings TO nexus_app;

-- ─────────── 2. member_notification_preferences ───────────

CREATE TABLE member_notification_preferences (
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id     uuid PRIMARY KEY,
  statement_email     boolean NOT NULL DEFAULT true,
  statement_sms       boolean NOT NULL DEFAULT false,
  transaction_alerts  boolean NOT NULL DEFAULT true,
  marketing           boolean NOT NULL DEFAULT false,
  preferred_language  text NOT NULL DEFAULT 'en'
    CHECK (preferred_language IN ('en','sw')),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  updated_by          uuid
);
CREATE INDEX member_notification_prefs_tenant_idx
  ON member_notification_preferences (tenant_id);

ALTER TABLE member_notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE member_notification_preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_member_notification_preferences ON member_notification_preferences
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON member_notification_preferences TO nexus_app;

-- ─────────── 3. tenant_operations.statement_mail_cron ───────────

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS statement_mail_cron text NOT NULL DEFAULT '0 6 1 */3 *';

COMMENT ON COLUMN tenant_operations.statement_mail_cron IS
  'Cron expression (5-field) that drives the statement-mailer worker schedule. Default = 6am on the 1st of every 3rd month (quarterly).';

COMMIT;
