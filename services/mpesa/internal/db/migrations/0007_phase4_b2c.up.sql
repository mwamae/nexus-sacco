-- Phase 4 — B2C outbound payments.
--
-- New columns + tables this phase needs:
--   • mpesa_paybills.is_default — per-tenant flag picking the "use
--     this paybill for B2C unless told otherwise" account. Multiple
--     active paybills with is_default=true are not enforced at the
--     DB level (no partial-unique) because tenants legitimately
--     migrate defaults mid-flight; the savings handler picks the
--     most-recently-updated row when multiple are flagged.
--   • mpesa_outbound_status += 'reversed' — Safaricom-initiated
--     reversal of a successfully-delivered B2C.
--   • mpesa_outbound_requests: result_raw jsonb, finalization_status,
--     finalization_attempts, finalized_at — track the savings-side
--     callback retry. The dispatcher marks the outbound row as
--     completed when Daraja confirms; the finalize-disbursement HTTP
--     call to savings is tracked separately so we can retry it
--     without re-asking Daraja.
--   • CoA codes 1015 + 1099 seeded for every tenant.
--   • Workflow definition mpesa_b2c_reversal seeded for every tenant.
--
-- All additive. No destructive operations.

-- ─────────── mpesa_paybills.is_default ───────────
ALTER TABLE mpesa_paybills
  ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS mpesa_paybills_default_idx
  ON mpesa_paybills (tenant_id, is_default, status)
  WHERE is_default = true AND status = 'active';

-- ─────────── mpesa_outbound_status += 'reversed' ───────────
DO $$ BEGIN
  ALTER TYPE mpesa_outbound_status ADD VALUE IF NOT EXISTS 'reversed';
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ─────────── mpesa_outbound_requests: callback bookkeeping ───────────
DO $$ BEGIN
  CREATE TYPE mpesa_outbound_finalization_status AS ENUM (
    'pending', 'in_progress', 'completed', 'failed'
  );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE mpesa_outbound_requests
  ADD COLUMN IF NOT EXISTS result_raw            jsonb,
  ADD COLUMN IF NOT EXISTS mpesa_receipt_number  text,
  ADD COLUMN IF NOT EXISTS finalization_status   mpesa_outbound_finalization_status NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS finalization_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS finalized_at          timestamptz,
  ADD COLUMN IF NOT EXISTS finalization_error    text,
  ADD COLUMN IF NOT EXISTS locked_at             timestamptz,
  ADD COLUMN IF NOT EXISTS locked_by             uuid;

-- Dispatcher poll index. Picks the oldest 'pending' row first;
-- attempts < some-ceiling caps poisoned rows.
CREATE INDEX IF NOT EXISTS mpesa_outbound_requests_dispatch_idx
  ON mpesa_outbound_requests (tenant_id, status, requested_at)
  WHERE status = 'pending';

-- Finalize-retry index — used by the reconciler that re-runs the
-- savings finalize call when the initial attempt failed but Daraja
-- already confirmed.
CREATE INDEX IF NOT EXISTS mpesa_outbound_requests_finalize_idx
  ON mpesa_outbound_requests (tenant_id, finalization_status, completed_at)
  WHERE status = 'completed' AND finalization_status IN ('pending', 'failed');

-- ─────────── CoA seed (per tenant) ───────────
-- 1015 — M-PESA Paybill Cash (per-paybill sub-accounting via narration tags)
-- 1099 — M-PESA Clearing (phase 3 staging account for inbound; phase 4
--        adds it formally so the GL can resolve it without manual seed)
DO $$
DECLARE
  t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP

  -- 1015
  INSERT INTO chart_of_accounts (tenant_id, code, name, class, type, normal_balance, is_active, description)
  SELECT t_id, '1015', 'M-PESA Paybill Cash', 'asset', 'detail', 'debit', true,
         'Cash sitting on a Safaricom paybill. One per active paybill; sub-accounted via posting narrations (paybill shortcode).'
  WHERE NOT EXISTS (
    SELECT 1 FROM chart_of_accounts WHERE tenant_id = t_id AND code = '1015'
  );

  -- 1099
  INSERT INTO chart_of_accounts (tenant_id, code, name, class, type, normal_balance, is_active, description)
  SELECT t_id, '1099', 'M-PESA Clearing', 'asset', 'detail', 'debit', true,
         'Holding account for M-PESA inbounds awaiting distribution and for B2C debits awaiting Daraja confirmation.'
  WHERE NOT EXISTS (
    SELECT 1 FROM chart_of_accounts WHERE tenant_id = t_id AND code = '1099'
  );

END LOOP;
END $$;

-- ─────────── Workflow seed: mpesa_b2c_reversal ───────────
-- Recreate the _wf_seed_one helper inline (workflow's 0003 drops it
-- end-of-script + each consumer migration reinstates it). Same idiom
-- as phase-2's mpesa_unallocated_reconciliation seed.
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
DECLARE
  t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP
  PERFORM _wf_seed_one(t_id, 'mpesa_b2c_reversal',
    'M-PESA B2C reversal',
    'Safaricom reversed a B2C outbound payment after delivery. Staff must decide whether to retry the disbursement or cancel the loan.',
    $j$[
      {"name":"Reconciler","roles":["branch_manager","accountant"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);
END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one(uuid, text, text, text, jsonb);
