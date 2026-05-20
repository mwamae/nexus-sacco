-- Approval workflow engine. Entity-agnostic: any module creates an
-- instance against a workflow definition by subject_kind+subject_id and
-- the engine drives the level-by-level approval, calling back a webhook
-- on terminal state.

CREATE TYPE wf_status AS ENUM (
  'pending', 'in_progress', 'approved', 'rejected',
  'returned', 'awaiting_info', 'escalated', 'cancelled', 'expired'
);

CREATE TYPE wf_level_status AS ENUM (
  'waiting', 'in_progress', 'approved', 'rejected',
  'returned', 'awaiting_info', 'escalated', 'skipped'
);

CREATE TYPE wf_action_kind AS ENUM (
  'create', 'approve', 'reject', 'return', 'request_info', 'resume',
  'escalate', 'reassign', 'cancel', 'callback_fired', 'sla_breached'
);

CREATE TYPE wf_quorum AS ENUM ('any_one', 'all');

-- ═══════════════════════════════════════════════════════════════════
-- wf_definitions — a workflow template per (tenant, process_kind, version).
-- Editing a definition creates a new version row; instances reference
-- the specific definition version they were started against.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE wf_definitions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  process_kind    text NOT NULL,                -- "member_onboarding", "loan_disbursement", …
  name            text NOT NULL,
  description     text,
  version         int  NOT NULL DEFAULT 1,
  active          boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  -- At most one active definition per (tenant, process_kind).
  CONSTRAINT wf_definitions_version_positive CHECK (version >= 1)
);
CREATE UNIQUE INDEX wf_definitions_active_idx
  ON wf_definitions (tenant_id, process_kind) WHERE active = true;
CREATE INDEX wf_definitions_tenant_kind_idx ON wf_definitions (tenant_id, process_kind);
CREATE TRIGGER wf_definitions_updated_at BEFORE UPDATE ON wf_definitions
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ═══════════════════════════════════════════════════════════════════
-- wf_levels — ordered approval levels within a definition.
-- approver_roles and approver_user_ids together describe who can act
-- at this level. condition_expr is a small JSONLogic expression
-- evaluated against the instance context to decide if the level
-- applies; null means always-on.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE wf_levels (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  definition_id      uuid NOT NULL REFERENCES wf_definitions(id) ON DELETE CASCADE,
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  level_order        int  NOT NULL,
  name               text NOT NULL,             -- "Maker", "Checker", "Branch Approver"
  approver_roles     text[]      NOT NULL DEFAULT '{}',
  approver_user_ids  uuid[]      NOT NULL DEFAULT '{}',
  quorum             wf_quorum   NOT NULL DEFAULT 'any_one',
  condition_expr     jsonb,                     -- null = always applies
  sla_hours          int,                       -- nullable = no SLA
  escalation_role    text,                      -- on SLA breach
  escalation_user_id uuid,
  UNIQUE (definition_id, level_order)
);
CREATE INDEX wf_levels_definition_idx ON wf_levels (definition_id, level_order);

-- ═══════════════════════════════════════════════════════════════════
-- wf_instances — a live approval request.
-- Snapshots the definition_id so editing the definition doesn't
-- retroactively change running instances. Per-level state is in the
-- jsonb levels array so we don't need a per-level row.
--   levels = [
--     { "order":0, "name":"Maker", "status":"approved",
--       "approver_roles":[...], "approver_user_ids":[...],
--       "quorum":"any_one", "condition":null,
--       "sla_hours":24, "sla_due_at":"...", "actions":[...]
--     }, ...
--   ]
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE wf_instances (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  definition_id     uuid NOT NULL REFERENCES wf_definitions(id) ON DELETE RESTRICT,
  process_kind      text NOT NULL,             -- denormalised for filtering
  subject_kind      text NOT NULL,             -- "member", "loan", "withdrawal", …
  subject_id        text NOT NULL,
  status            wf_status NOT NULL DEFAULT 'pending',
  current_level     int  NOT NULL DEFAULT 0,
  context           jsonb NOT NULL DEFAULT '{}'::jsonb,  -- input data for condition eval + UI display
  callback_url      text,                      -- POSTed on terminal state
  callback_secret   text,                      -- optional shared secret
  callback_status   text,                      -- "pending" | "delivered" | "failed:<msg>"
  callback_delivered_at timestamptz,
  initiator_id      uuid,
  started_at        timestamptz NOT NULL DEFAULT now(),
  completed_at      timestamptz,
  -- snapshot of levels at creation; per-level state mutates here as
  -- approvers act.
  levels            jsonb NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX wf_instances_tenant_status_idx ON wf_instances (tenant_id, status);
CREATE INDEX wf_instances_subject_idx       ON wf_instances (tenant_id, subject_kind, subject_id);
CREATE INDEX wf_instances_process_idx       ON wf_instances (tenant_id, process_kind, status);

-- ═══════════════════════════════════════════════════════════════════
-- wf_actions — audit row per approver action.
-- Mirrored into the global audit_log table for cross-system queries,
-- but kept in its own table so the workflow UI can render a clean
-- timeline without filtering noise.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE wf_actions (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  instance_id   uuid NOT NULL REFERENCES wf_instances(id) ON DELETE CASCADE,
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  level_order   int,                           -- null for instance-level actions (create, cancel)
  action        wf_action_kind NOT NULL,
  actor_id      uuid,
  actor_role    text,                          -- snapshot of which role they were acting under
  comments      text,
  metadata      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wf_actions_instance_idx ON wf_actions (instance_id, created_at);
CREATE INDEX wf_actions_actor_idx    ON wf_actions (tenant_id, actor_id, created_at);

-- ═══════════════════════════════════════════════════════════════════
-- wf_delegations — forward-prep for vacation handover. Not yet wired
-- into assignment resolution; included so the schema doesn't need to
-- change when we light up runtime delegation.
-- ═══════════════════════════════════════════════════════════════════
CREATE TABLE wf_delegations (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  delegator_id    uuid NOT NULL,                -- user delegating away
  delegate_id     uuid NOT NULL,                -- user receiving delegation
  effective_from  timestamptz NOT NULL,
  effective_to    timestamptz NOT NULL,
  scope_kinds     text[] NOT NULL DEFAULT '{}', -- empty = all process kinds
  reason          text,
  active          boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wf_delegations_lookup_idx
  ON wf_delegations (tenant_id, delegator_id, effective_from, effective_to) WHERE active = true;

-- ═══════════════════════════════════════════════════════════════════
-- RLS — every table tenant-scoped via app.tenant_id.
-- ═══════════════════════════════════════════════════════════════════
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['wf_definitions','wf_levels','wf_instances','wf_actions','wf_delegations'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE  ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON
  wf_definitions, wf_levels, wf_instances, wf_actions, wf_delegations
TO nexus_app;
