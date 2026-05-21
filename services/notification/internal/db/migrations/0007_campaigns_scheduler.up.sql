-- ═══════════════════════════════════════════════════════════════════
-- Stage 7 — Campaigns + scheduled jobs.
--
-- Campaigns:
--   • notification_campaigns       — bulk dispatch definition.
--   • notification_campaign_runs   — execution log (1 per send attempt).
--   • notification_campaign_settings — per-tenant maker/checker threshold.
--
-- Scheduler:
--   • notification_scheduled_jobs  — registered recurring jobs with cron.
--   • notification_job_runs        — execution log per job tick.
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Campaigns ───────────

CREATE TYPE campaign_status AS ENUM (
  'draft',              -- created, not validated
  'awaiting_approval',  -- recipients exceed maker/checker threshold
  'scheduled',          -- queued for future send
  'sending',            -- worker is dispatching
  'sent',               -- all recipients processed
  'cancelled',          -- aborted before send
  'failed'              -- terminal failure during send
);

CREATE TABLE IF NOT EXISTS notification_campaigns (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name                     text NOT NULL,
  description              text,
  event_code               text NOT NULL,                       -- which event drives the template
  -- Channels to dispatch over. Must be a subset of the event's
  -- configured channels — the dispatcher skips unsupported channels.
  channels                 notification_channel[] NOT NULL DEFAULT ARRAY['in_app']::notification_channel[],
  -- Audience filter as JSON.  Shape varies by `type`:
  --   {"type":"all_members"}
  --   {"type":"status",          "status":"active"}
  --   {"type":"active_loans"}
  --   {"type":"loan_defaulters", "dpd_min":30, "dpd_max":90}
  --   {"type":"custom_list",     "member_ids":["uuid",…]}
  audience                 jsonb NOT NULL DEFAULT '{"type":"all_members"}'::jsonb,
  -- Variables injected on every recipient's template render. Per-member
  -- variables (member_no, full_name, etc.) are populated by the worker.
  payload                  jsonb NOT NULL DEFAULT '{}'::jsonb,
  status                   campaign_status NOT NULL DEFAULT 'draft',
  scheduled_for            timestamptz,
  estimated_recipients     int  NOT NULL DEFAULT 0,
  -- Progress counters (updated as the worker dispatches).
  total_recipients         int  NOT NULL DEFAULT 0,
  dispatched_count         int  NOT NULL DEFAULT 0,
  failed_count             int  NOT NULL DEFAULT 0,
  -- Maker/checker
  created_at               timestamptz NOT NULL DEFAULT now(),
  created_by               uuid,
  approved_at              timestamptz,
  approved_by              uuid,
  sent_at                  timestamptz,
  cancelled_at             timestamptz,
  cancel_reason            text,
  failure_reason           text,
  updated_at               timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_campaigns_due_idx
  ON notification_campaigns (status, scheduled_for)
  WHERE status = 'scheduled';
CREATE INDEX IF NOT EXISTS notification_campaigns_tenant_idx
  ON notification_campaigns (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notification_campaign_runs (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  campaign_id      uuid NOT NULL REFERENCES notification_campaigns(id) ON DELETE CASCADE,
  started_at       timestamptz NOT NULL DEFAULT now(),
  finished_at      timestamptz,
  recipients_total int NOT NULL DEFAULT 0,
  dispatched_count int NOT NULL DEFAULT 0,
  failed_count     int NOT NULL DEFAULT 0,
  notes            text
);
CREATE INDEX IF NOT EXISTS notification_campaign_runs_campaign_idx
  ON notification_campaign_runs (campaign_id, started_at DESC);

CREATE TABLE IF NOT EXISTS notification_campaign_settings (
  tenant_id                 uuid PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  -- Above this recipient count, the campaign must be approved by a
  -- second user before it can be sent. 0 = no approval required.
  approval_recipient_threshold int NOT NULL DEFAULT 500,
  updated_at                timestamptz NOT NULL DEFAULT now()
);
INSERT INTO notification_campaign_settings (tenant_id)
SELECT id FROM tenants
ON CONFLICT (tenant_id) DO NOTHING;

-- ─────────── Scheduled jobs ───────────

CREATE TABLE IF NOT EXISTS notification_scheduled_jobs (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  job_key      text NOT NULL,        -- registered handler key, e.g. 'loan_repayment_reminders'
  description  text NOT NULL DEFAULT '',
  cron_expr    text NOT NULL,        -- 5-field cron expression (m h dom mon dow)
  is_active    boolean NOT NULL DEFAULT true,
  -- Job-specific config (e.g. {"days_ahead":[3,1,0]} for repayment reminders).
  config       jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_run_at  timestamptz,
  next_run_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, job_key)
);
CREATE INDEX IF NOT EXISTS notification_scheduled_jobs_due_idx
  ON notification_scheduled_jobs (next_run_at)
  WHERE is_active = true;

CREATE TABLE IF NOT EXISTS notification_job_runs (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scheduled_job_id  uuid NOT NULL REFERENCES notification_scheduled_jobs(id) ON DELETE CASCADE,
  job_key           text NOT NULL,
  scheduled_for     timestamptz NOT NULL,
  started_at        timestamptz NOT NULL DEFAULT now(),
  finished_at       timestamptz,
  records_processed int NOT NULL DEFAULT 0,
  records_failed    int NOT NULL DEFAULT 0,
  status            text NOT NULL DEFAULT 'running'
                      CHECK (status IN ('running', 'succeeded', 'failed')),
  error_message     text
);
CREATE INDEX IF NOT EXISTS notification_job_runs_job_idx
  ON notification_job_runs (scheduled_job_id, started_at DESC);

-- ─────────── RLS + grants ───────────

DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'notification_campaigns', 'notification_campaign_runs',
    'notification_campaign_settings',
    'notification_scheduled_jobs', 'notification_job_runs'
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
  notification_campaigns, notification_campaign_runs,
  notification_campaign_settings,
  notification_scheduled_jobs, notification_job_runs
TO nexus_app;

-- ─────────── Seed default jobs ───────────
--
-- Two recurring jobs per tenant ship with stage 7. Tenants can disable
-- or re-cron them from the admin UI.
--
--   loan_repayment_reminders   — daily at 08:00. Fires
--     LOAN_INSTALLMENT_DUE per loan whose next installment is due
--     in the days listed in config.days_ahead (default [3,1,0]).
--   dormancy_warnings          — daily at 09:00. Fires
--     DORMANCY_WARNING per member who hasn't transacted in the last
--     config.warning_days days (default 90).

INSERT INTO notification_scheduled_jobs (tenant_id, job_key, description, cron_expr, config, next_run_at)
SELECT
  t.id,
  'loan_repayment_reminders',
  'Daily reminder for loans whose next installment is approaching',
  '0 8 * * *',
  '{"days_ahead":[3,1,0]}'::jsonb,
  date_trunc('day', now()) + interval '1 day' + interval '8 hours'
FROM tenants t
ON CONFLICT (tenant_id, job_key) DO NOTHING;

INSERT INTO notification_scheduled_jobs (tenant_id, job_key, description, cron_expr, config, next_run_at)
SELECT
  t.id,
  'dormancy_warnings',
  'Daily warning for members approaching the dormancy threshold',
  '0 9 * * *',
  '{"warning_days":90}'::jsonb,
  date_trunc('day', now()) + interval '1 day' + interval '9 hours'
FROM tenants t
ON CONFLICT (tenant_id, job_key) DO NOTHING;
