-- Unified Inbox (PR #6): pending workflow gate for the fiscal-year
-- close action. fiscal_year_closes is the audit row that exists
-- AFTER a successful close — we need a separate table to track the
-- proposed close + its workflow_instance_id BEFORE the actual close
-- fires, so the Inbox has something to point at.
--
-- Lifecycle:
--   1. Operator hits POST /v1/fiscal-years/{year}/submit-for-close →
--      inserts a fiscal_year_close_proposals row with status='pending'
--      + workflow_instance_id from the engine.
--   2. Board approver decides in /approvals.
--   3. Engine fires the callback into
--      POST /internal/v1/fiscal-year-close-proposals/{id}/resolve.
--   4. On approve → the resolve handler runs the existing Close
--      logic + sets status='applied' + applied_close_id.
--   5. On reject  → status='rejected'.
--
-- The partial unique index ensures at most one pending proposal per
-- (tenant, year). The legacy Close endpoint stays open for tenants
-- without unified_inbox_enabled.

CREATE TABLE IF NOT EXISTS fiscal_year_close_proposals (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  year                 int  NOT NULL,
  workflow_instance_id uuid NOT NULL,
  notes                text,
  submitted_by         uuid NOT NULL,
  submitted_at         timestamptz NOT NULL DEFAULT now(),
  status               text NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'applied')),
  applied_close_id     uuid REFERENCES fiscal_year_closes(id) ON DELETE SET NULL,
  resolved_at          timestamptz,
  resolved_note        text
);

CREATE UNIQUE INDEX IF NOT EXISTS fiscal_year_close_proposals_one_pending
  ON fiscal_year_close_proposals (tenant_id, year)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS fiscal_year_close_proposals_workflow_idx
  ON fiscal_year_close_proposals (workflow_instance_id);

CREATE INDEX IF NOT EXISTS fiscal_year_close_proposals_tenant_year_idx
  ON fiscal_year_close_proposals (tenant_id, year, status);

ALTER TABLE fiscal_year_close_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE fiscal_year_close_proposals FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_fyc_proposals ON fiscal_year_close_proposals
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON fiscal_year_close_proposals TO nexus_app;
