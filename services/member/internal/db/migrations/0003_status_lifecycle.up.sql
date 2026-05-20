-- Member lifecycle statuses: rebuild the enum from
--   pending | active | suspended | locked | closed | rejected
-- to the canonical 8-state set used by the SACCO platform:
--   pending | active | dormant | suspended | blacklisted | exited | deceased | rejected
-- Existing `locked` rows roll into `suspended`; `closed` rolls into `exited`.

CREATE TYPE member_status_v2 AS ENUM (
  'pending', 'active', 'dormant', 'suspended',
  'blacklisted', 'exited', 'deceased', 'rejected'
);

ALTER TABLE members
  ALTER COLUMN status DROP DEFAULT,
  ALTER COLUMN status TYPE member_status_v2 USING (
    CASE status::text
      WHEN 'locked' THEN 'suspended'::member_status_v2
      WHEN 'closed' THEN 'exited'::member_status_v2
      ELSE status::text::member_status_v2
    END
  ),
  ALTER COLUMN status SET DEFAULT 'pending'::member_status_v2;

DROP TYPE member_status;
ALTER TYPE member_status_v2 RENAME TO member_status;

-- ─────────── Audit + workflow-tracking columns on members ───────────
ALTER TABLE members
  ADD COLUMN status_changed_at        timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN last_activity_at         timestamptz,
  ADD COLUMN dormancy_warning_sent_at timestamptz,
  ADD COLUMN dormancy_threshold_at    timestamptz,   -- snapshot of threshold at last calc
  ADD COLUMN blacklist_reason         text,
  ADD COLUMN blacklist_authorized_by  uuid,
  ADD COLUMN blacklisted_at           timestamptz,
  ADD COLUMN deceased_at              timestamptz,
  ADD COLUMN deceased_notified_at     timestamptz,
  ADD COLUMN exit_initiated_at        timestamptz,
  ADD COLUMN exit_completed_at        timestamptz;

-- Backfill status_changed_at to created_at so existing members aren't
-- treated as "just changed".
UPDATE members SET status_changed_at = created_at;

CREATE INDEX members_last_activity_idx ON members (tenant_id, last_activity_at)
  WHERE status = 'active';

-- ─────────── Status-change audit table ───────────
-- Captures every transition: who, when, from→to, reason category,
-- free-text note, optional supporting document path, and the workflow
-- instance id when the change went through the approval engine.

CREATE TYPE member_status_reason AS ENUM (
  'onboarding_approval',
  'onboarding_rejection',
  'dormancy_inactivity',
  'reactivation_request',
  'loan_default',
  'compliance_hold',
  'disciplinary_action',
  'fraud_investigation',
  'regulatory_directive',
  'member_request',
  'admin_action',
  'deceased_notification',
  'system_correction',
  'other'
);

CREATE TABLE member_status_changes (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_id            uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  from_status          member_status,                  -- null for very-first record (creation)
  to_status            member_status NOT NULL,
  reason_category      member_status_reason NOT NULL DEFAULT 'admin_action',
  reason_note          text,
  supporting_doc_path  text,
  supporting_doc_mime  text,
  changed_by           uuid,
  changed_at           timestamptz NOT NULL DEFAULT now(),
  workflow_instance_id uuid,                           -- when routed through workflow engine
  review_date          date                            -- optional; for time-bound suspensions
);
CREATE INDEX member_status_changes_member_idx ON member_status_changes (member_id, changed_at DESC);
CREATE INDEX member_status_changes_tenant_idx ON member_status_changes (tenant_id, changed_at DESC);
CREATE INDEX member_status_changes_workflow_idx ON member_status_changes (workflow_instance_id)
  WHERE workflow_instance_id IS NOT NULL;

-- Track which workflow_instance ids represent *pending* status-change
-- proposals (so the callback can find the right one).
CREATE TABLE member_status_proposals (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_id            uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  workflow_instance_id uuid NOT NULL UNIQUE,
  proposed_status      member_status NOT NULL,
  reason_category      member_status_reason NOT NULL,
  reason_note          text,
  supporting_doc_path  text,
  supporting_doc_mime  text,
  review_date          date,
  proposed_by          uuid,
  proposed_at          timestamptz NOT NULL DEFAULT now(),
  resolved_at          timestamptz,
  resolution           text                             -- "approved"|"rejected"|"cancelled"
);
CREATE INDEX member_status_proposals_member_idx ON member_status_proposals (member_id)
  WHERE resolved_at IS NULL;

-- ─────────── RLS ───────────
ALTER TABLE member_status_changes   ENABLE ROW LEVEL SECURITY;
ALTER TABLE member_status_changes   FORCE  ROW LEVEL SECURITY;
ALTER TABLE member_status_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE member_status_proposals FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_status_changes ON member_status_changes
  USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

CREATE POLICY tenant_isolation_status_proposals ON member_status_proposals
  USING (tenant_id = current_tenant_id()) WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON member_status_changes, member_status_proposals TO nexus_app;

-- ─────────── Supporting-doc kinds for status changes ───────────
-- The existing document_kind enum gets three additions so the
-- existing org/member document upload pipeline can carry these too.
-- (The actual upload reuses the per-member storage that already exists.)
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'death_certificate';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'exit_clearance';
ALTER TYPE document_kind ADD VALUE IF NOT EXISTS 'blacklist_directive';

-- ─────────── Tenant-side dormancy threshold ───────────
-- Lives on tenant_operations next to the existing lending/savings
-- defaults so tenant_owner / sacco_admin can edit it from Settings.
ALTER TABLE tenant_operations
  ADD COLUMN dormancy_threshold_days int NOT NULL DEFAULT 365;
