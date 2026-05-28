-- System Health PR — worker heartbeats + operations-view permission.
--
-- Two pieces:
--
-- 1) worker_heartbeats — a tiny ledger continuous workers
--    (posting-dispatcher, b2c-dispatcher, distributor) update on
--    every tick. The system-health aggregator reads this to
--    classify each worker as ok / degraded / down based on how
--    fresh `last_beat_at` is. Single global table (no tenant
--    scoping) because workers are platform-wide, not per-tenant.
--
-- 2) tenant:operations:view — new permission gating the
--    /operations/system-health admin page. Granted to tenant_owner
--    and sacco_admin only — operators see infra health; tellers
--    and accountants don't need it. Auditors get it too since
--    they're often the ones spotting outbox lag.

CREATE TABLE IF NOT EXISTS worker_heartbeats (
    worker_name  text PRIMARY KEY,
    last_beat_at timestamptz NOT NULL DEFAULT now(),
    hostname     text,
    version      text,
    details      jsonb
);

GRANT SELECT, INSERT, UPDATE ON worker_heartbeats TO nexus_app;

INSERT INTO permissions (code, description, category) VALUES
  ('tenant:operations:view', 'View the System Health dashboard + infrastructure status', 'operations')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_code) VALUES
  -- tenant_owner
  ('00000000-0000-0000-0000-000000000002', 'tenant:operations:view'),
  -- sacco_admin
  ('00000000-0000-0000-0000-000000000003', 'tenant:operations:view'),
  -- auditor (read-only role — id 8)
  ('00000000-0000-0000-0000-000000000008', 'tenant:operations:view')
ON CONFLICT (role_id, permission_code) DO NOTHING;
