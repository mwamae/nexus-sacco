-- Phase 2 of the M-PESA integration: catch the money.
--
-- This migration adds:
--   • mpesa_paybills.strict_validation        — opt-in C2B00012 on unknown account
--   • mpesa_paybills.allow_msisdn_fallback    — opt-in phone-number resolver branch
--   • mpesa_paybills.webhook_token            — per-paybill shared secret Safaricom
--                                                appends to the callback URL
--   • mpesa_inbound_events.status             — lifecycle: received / distributed / failed
--   • mpesa_inbound_events.resolved_member_id — counterparty.id the resolver picked
--   • mpesa_inbound_events.resolved_via       — which branch matched (or 'unallocated')
--   • mpesa_inbound_events.workflow_instance_id — soft ref to the reconciliation task
--                                                  when unallocated (NULL otherwise)
--   • indexes the staff query + resolver actually need
--   • mpesa_unallocated_reconciliation workflow definition seeded per tenant
--
-- Notes:
--   • `transaction_id` (phase 1) IS the Safaricom MpesaReceiptNumber. The
--     phase 2 spec talks about "mpesa_receipt_number"; same column.
--   • The UNIQUE (tenant_id, transaction_id) from phase 1 absorbs Safaricom
--     retries (the confirmation handler uses INSERT ... ON CONFLICT DO NOTHING).
--   • The wf seed re-creates the _wf_seed_one helper inline because the
--     workflow service migration 0003 dropped it at end-of-script.

-- ─────────── enums (idempotent) ───────────
DO $$ BEGIN
  CREATE TYPE mpesa_inbound_status AS ENUM ('received', 'distributed', 'failed');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
  CREATE TYPE mpesa_resolved_via AS ENUM (
    'member_no', 'cp_number', 'loan_no', 'deposit_account_no', 'msisdn', 'unallocated'
  );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ─────────── mpesa_paybills column additions ───────────
ALTER TABLE mpesa_paybills
  ADD COLUMN IF NOT EXISTS strict_validation     boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS allow_msisdn_fallback boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS webhook_token         text;

-- Backfill any existing paybills with a fresh token, then enforce NOT NULL +
-- UNIQUE. Postgres requires the constraint to be added after the backfill
-- because NOT NULL fails on rows that haven't received a value yet.
UPDATE mpesa_paybills
   SET webhook_token = encode(gen_random_bytes(24), 'hex')
 WHERE webhook_token IS NULL;

ALTER TABLE mpesa_paybills ALTER COLUMN webhook_token SET NOT NULL;

DO $$ BEGIN
  ALTER TABLE mpesa_paybills
    ADD CONSTRAINT mpesa_paybills_webhook_token_key UNIQUE (webhook_token);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- ─────────── mpesa_inbound_events column additions ───────────
ALTER TABLE mpesa_inbound_events
  ADD COLUMN IF NOT EXISTS status               mpesa_inbound_status NOT NULL DEFAULT 'received',
  ADD COLUMN IF NOT EXISTS resolved_member_id   uuid,
  ADD COLUMN IF NOT EXISTS resolved_via         mpesa_resolved_via,
  ADD COLUMN IF NOT EXISTS workflow_instance_id uuid;

-- ─────────── indexes ───────────
CREATE INDEX IF NOT EXISTS mpesa_inbound_events_paybill_time_idx
  ON mpesa_inbound_events (tenant_id, paybill_id, transaction_time DESC);

-- transaction_id already carries a UNIQUE (tenant_id, transaction_id) from
-- phase 1, so the per-receipt lookup is covered. A separate non-unique
-- index would just duplicate the constraint's btree.

CREATE INDEX IF NOT EXISTS mpesa_inbound_events_resolved_member_idx
  ON mpesa_inbound_events (tenant_id, resolved_member_id)
  WHERE resolved_member_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS mpesa_inbound_events_status_idx
  ON mpesa_inbound_events (tenant_id, status, received_at DESC);

-- ─────────── workflow seed ───────────
-- Recreate the helper inline (workflow's 0003 dropped it) so we can use
-- the same idiom + the same idempotency rules. Dropped at end-of-script.
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
  SELECT id INTO v_def_id
    FROM wf_definitions
   WHERE tenant_id = p_tenant_id
     AND process_kind = p_process_kind
     AND active = true
   LIMIT 1;
  IF v_def_id IS NOT NULL THEN
    RETURN;
  END IF;

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

-- Seed the unallocated-payment reconciliation workflow for every tenant.
-- Single level, any-one quorum across branch_manager + accountant, 24h SLA.
-- Not technically maker-checker — there's no two-eyes principle, but the
-- engine treats a single-level definition as "needs a human" and routes it
-- into the Unified Inbox the same way as any other task.
DO $$
DECLARE
  t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP
  PERFORM _wf_seed_one(t_id, 'mpesa_unallocated_reconciliation',
    'M-PESA unallocated reconciliation',
    'A C2B payment arrived that the resolver could not match to a member. Staff must pick the right member, mark for refund, or close as a duplicate.',
    $j$[
      {"name":"Reconciler","roles":["branch_manager","accountant"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);
END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one(uuid, text, text, text, jsonb);
