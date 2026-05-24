-- Unified Inbox (PR #6): per-run tracking row for bulk dormancy.
--
-- Without a workflow gate, the dormancy run today computes
-- candidates and applies them in one shot. With the Inbox, we need
-- to capture the candidate set AT SUBMIT TIME (so the eventual apply
-- hits the same members even if their last_activity_at has moved
-- between submit and approve), and we need somewhere to put the
-- workflow_instance_id back-link.
--
-- Lifecycle:
--   1. POST /v1/members/dormancy/submit-for-approval →
--      computes candidates, inserts a dormancy_runs row with
--      status='pending' + snapshot jsonb + threshold + workflow id.
--   2. Board decides in /approvals.
--   3. POST /internal/v1/members/dormancy/{run_id}/resolve →
--      on approve, loops the snapshotted candidates through the
--      existing Status.ApplyTx; sets status='applied' + applied_at.
--   4. On reject → status='rejected'.

CREATE TABLE IF NOT EXISTS dormancy_runs (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  workflow_instance_id uuid NOT NULL,
  threshold_days       int  NOT NULL,
  -- snapshot is the array of {counterparty_id, member_no, full_name,
  -- last_activity_at, days_inactive} taken at submit time. The apply
  -- step iterates this list, not a fresh query.
  snapshot             jsonb NOT NULL,
  candidate_count      int  NOT NULL,
  status               text NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'applied')),
  submitted_by         uuid NOT NULL,
  submitted_at         timestamptz NOT NULL DEFAULT now(),
  resolved_at          timestamptz,
  applied_at           timestamptz,
  resolved_note        text,
  -- Per-counterparty apply outcomes for audit (e.g. "skipped: state
  -- drifted to suspended"). Same shape as snapshot but with an extra
  -- "outcome" field; empty until apply runs.
  apply_outcomes       jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS dormancy_runs_workflow_idx
  ON dormancy_runs (workflow_instance_id);

CREATE INDEX IF NOT EXISTS dormancy_runs_tenant_status_idx
  ON dormancy_runs (tenant_id, status, submitted_at DESC);

ALTER TABLE dormancy_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE dormancy_runs FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_dormancy_runs ON dormancy_runs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON dormancy_runs TO nexus_app;
