-- Phase 6 — daily statement-pull reconciliation schema.
--
-- Two new tables:
--   mpesa_statement_pulls       — one row per paybill, per day, per
--                                 statement window. Carries the
--                                 Safaricom Account Balance + the
--                                 totals we reconcile against.
--   mpesa_reconciliation_diffs  — one row per discrepancy found
--                                 between Safaricom's statement and
--                                 our ledger (mpesa_inbound_events
--                                 + mpesa_outbound_requests). The
--                                 reconciler raises an
--                                 mpesa_reconciliation_diff wf task
--                                 per row so staff can investigate.
--
-- Plus the mpesa_reconciliation_diff workflow definition seeded per
-- tenant so the reconciler's task-create call has somewhere to land.

-- ─────────── enums ───────────
DO $$ BEGIN
  CREATE TYPE mpesa_statement_pull_status AS ENUM ('pending', 'completed', 'failed');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE TYPE mpesa_diff_kind AS ENUM (
    'inbound_in_statement_missing_ledger',     -- Safaricom shows the receipt; we don't
    'outbound_in_statement_missing_ledger',    -- Safaricom shows the payout; we don't
    'inbound_in_ledger_missing_statement',     -- We have the row; Safaricom doesn't
    'outbound_in_ledger_missing_statement',
    'amount_mismatch'                           -- Both sides have the receipt but the amount differs
  );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE TYPE mpesa_diff_status AS ENUM ('open', 'resolved', 'ignored');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ─────────── mpesa_statement_pulls ───────────
CREATE TABLE IF NOT EXISTS mpesa_statement_pulls (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  paybill_id           uuid NOT NULL REFERENCES mpesa_paybills(id) ON DELETE CASCADE,
  window_from          timestamptz NOT NULL,
  window_to            timestamptz NOT NULL,
  status               mpesa_statement_pull_status NOT NULL DEFAULT 'pending',
  -- The reconciler tracks Daraja's async response separately from
  -- the request: requested_at is when we POSTed to AccountBalance,
  -- completed_at is when the Result callback (or an immediate
  -- sandbox response) landed.
  requested_at         timestamptz NOT NULL DEFAULT now(),
  completed_at         timestamptz,
  -- The raw Safaricom response. Daraja's AccountBalance Result
  -- envelope is wide; the reconciler picks fields out of result_raw
  -- when diffing.
  result_raw           jsonb,
  -- Headline reconciliation totals. Set when status flips to
  -- 'completed'. Each represents Safaricom's view of the same
  -- window's activity.
  daraja_inbound_total   numeric(18,2),
  daraja_outbound_total  numeric(18,2),
  daraja_closing_balance numeric(18,2),
  -- Our-side totals computed at the same time so the diff calc is
  -- deterministic — re-running the reconciler against the same
  -- window produces the same numbers regardless of new traffic.
  ledger_inbound_total   numeric(18,2),
  ledger_outbound_total  numeric(18,2),
  diff_count             integer NOT NULL DEFAULT 0,
  error_text             text,
  UNIQUE (tenant_id, paybill_id, window_from, window_to)
);
CREATE INDEX IF NOT EXISTS mpesa_statement_pulls_paybill_idx
  ON mpesa_statement_pulls (tenant_id, paybill_id, window_to DESC);

-- ─────────── mpesa_reconciliation_diffs ───────────
CREATE TABLE IF NOT EXISTS mpesa_reconciliation_diffs (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  statement_pull_id    uuid NOT NULL REFERENCES mpesa_statement_pulls(id) ON DELETE CASCADE,
  paybill_id           uuid NOT NULL REFERENCES mpesa_paybills(id) ON DELETE CASCADE,
  kind                 mpesa_diff_kind NOT NULL,
  status               mpesa_diff_status NOT NULL DEFAULT 'open',
  -- The Safaricom receipt number when known; nil for ledger-only
  -- diffs where our row doesn't have one yet.
  mpesa_receipt_number text,
  daraja_amount        numeric(18,2),
  ledger_amount        numeric(18,2),
  workflow_instance_id uuid,
  -- snapshot at diff-creation time so the reconciliation panel can
  -- render context without joining back to a maybe-deleted row.
  context              jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at           timestamptz NOT NULL DEFAULT now(),
  resolved_at          timestamptz,
  resolved_by          uuid,
  resolution_note      text
);
CREATE INDEX IF NOT EXISTS mpesa_reconciliation_diffs_open_idx
  ON mpesa_reconciliation_diffs (tenant_id, paybill_id, status, created_at DESC)
  WHERE status = 'open';

-- ─────────── RLS ───────────
ALTER TABLE mpesa_statement_pulls         ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_statement_pulls         FORCE  ROW LEVEL SECURITY;
ALTER TABLE mpesa_reconciliation_diffs    ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_reconciliation_diffs    FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation_mpesa_statement_pulls
  ON mpesa_statement_pulls
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
CREATE POLICY tenant_isolation_mpesa_reconciliation_diffs
  ON mpesa_reconciliation_diffs
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_statement_pulls      TO nexus_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON mpesa_reconciliation_diffs TO nexus_app;

-- ─────────── Workflow definition seed ───────────
CREATE OR REPLACE FUNCTION _wf_seed_one(
  p_tenant_id    uuid,
  p_process_kind text,
  p_name         text,
  p_description  text,
  p_levels       jsonb
) RETURNS void AS $$
DECLARE
  v_def_id uuid;
  v_level  jsonb;
  v_order  int := 0;
BEGIN
  SELECT id INTO v_def_id FROM wf_definitions
   WHERE tenant_id = p_tenant_id AND process_kind = p_process_kind AND active = true
   LIMIT 1;
  IF v_def_id IS NOT NULL THEN RETURN; END IF;

  INSERT INTO wf_definitions (tenant_id, process_kind, name, description, version, active)
  VALUES (p_tenant_id, p_process_kind, p_name, p_description, 1, true)
  RETURNING id INTO v_def_id;

  FOR v_level IN SELECT * FROM jsonb_array_elements(p_levels) LOOP
    INSERT INTO wf_levels (
      definition_id, tenant_id, level_order, name,
      approver_roles, approver_user_ids, quorum,
      condition_expr, sla_hours
    ) VALUES (
      v_def_id, p_tenant_id, v_order, v_level->>'name',
      ARRAY(SELECT jsonb_array_elements_text(v_level->'roles')),
      ARRAY[]::uuid[],
      (v_level->>'quorum')::wf_quorum,
      v_level->'condition',
      NULLIF((v_level->>'sla_hours')::text, '')::int
    );
    v_order := v_order + 1;
  END LOOP;
END $$ LANGUAGE plpgsql;

DO $$
DECLARE t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP
  PERFORM _wf_seed_one(t_id, 'mpesa_reconciliation_diff',
    'M-PESA reconciliation diff',
    'Daily reconciler found a discrepancy between Safaricom''s account statement and our ledger. Staff must investigate + reconcile.',
    $j$[
      {"name":"Reconciler","roles":["accountant","branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);
END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one(uuid, text, text, text, jsonb);
