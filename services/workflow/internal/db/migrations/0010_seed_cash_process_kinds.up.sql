-- Seed the remaining process_kind workflow definitions for every
-- existing tenant. Companion to 0003 — this fills in the cash /
-- share / loan / fee / member-exit kinds that the legacy
-- pending_approvals table was handling and that the unified-approvals
-- migration PR (one PR, six parts) now moves onto the workflow
-- engine.
--
-- Idempotent: _wf_seed_one no-ops when an active definition already
-- exists for (tenant_id, process_kind). Re-running this migration on
-- a tenant that's already seeded is safe.
--
-- New kinds in this migration:
--
--   share_lien            — pledging shares as collateral; Reviewer +
--                           Board because it changes encumbrance.
--   loan_disbursement     — moving approved loan principal to the
--                           member's account; Credit Officer + Reviewer.
--   loan_repayment        — manual repayment posting (not the
--                           collection-desk path which already routes
--                           through the receipt flow); single Checker.
--   loan_settle           — early payoff; Reviewer + Board because it
--                           closes a contract and may discount fees.
--   loan_reverse          — undoing a posted loan transaction;
--                           Board-only, never auto-execute.
--   fee_posting           — manual fee assessment from the collection
--                           desk; single Checker.
--   welfare_posting       — welfare-fund credit posting; single
--                           Checker.
--   application_fee       — member-application fee at registration;
--                           single Checker.
--   member_bosa_exit      — closing a BOSA-only member account; Board
--                           because it triggers the exit refund
--                           journal entry (executor itself is a
--                           loud-failing placeholder pending sign-off
--                           on the JE shape — see
--                           services/savings/internal/handler/wf_callbacks/member_bosa_exit.go).
--
-- Role mapping (mirrors 0003):
--   "Checker"        → branch_manager
--   "Reviewer"       → branch_manager
--   "Credit Officer" → credit_officer
--   "Board"          → [sacco_admin, tenant_owner]
--
-- Tenants can edit any of these in /workflows after migration. The
-- engine's "new version bumps and deactivates the old one" flow keeps
-- versioning honest.

-- Inline helper, dropped at the end so the schema stays clean.
CREATE OR REPLACE FUNCTION _wf_seed_one(
  p_tenant_id      uuid,
  p_process_kind   text,
  p_name           text,
  p_description    text,
  p_levels         jsonb
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

-- ─── Seed for every tenant ──────────────────────────────────────────
DO $$
DECLARE
  t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP

  -- ── Shares ────────────────────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'share_lien',
    'Share lien', 'Pledging shares as collateral — Reviewer then Board',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  -- ── Loans (originations + lifecycle events that aren't already in 0003) ──
  PERFORM _wf_seed_one(t_id, 'loan_disbursement',
    'Loan disbursement', 'Credit Officer originates → Reviewer signs off before principal moves',
    $j$[
      {"name":"Credit Officer","roles":["credit_officer"],"quorum":"any_one","sla_hours":24,"condition":null},
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_repayment',
    'Loan repayment (manual posting)', 'Maker-checker for direct loan repayment postings — the collection-desk receipt path routes through cash_deposit instead',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_settle',
    'Loan settlement (early payoff)', 'Reviewer + Board — closes the loan contract',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_reverse',
    'Loan transaction reversal', 'Board only — reversals never auto-execute',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  -- ── Fees + welfare ────────────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'fee_posting',
    'Manual fee posting', 'Single Checker on manually-assessed fees from the collection desk',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'welfare_posting',
    'Welfare-fund posting', 'Single Checker on welfare-fund credit postings',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'application_fee',
    'Member application fee', 'Single Checker on application fees collected at registration',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  -- ── Member lifecycle — BOSA exit ──────────────────────────────
  -- Genuine Board decision; tenants can tighten quorum to 'all' for
  -- full-Board sign-off later. Note: the executor is a loud-failing
  -- placeholder pending finance sign-off on the equity-refund JE shape;
  -- the dispatcher will surface the failure via callback_status =
  -- 'failed:…' rather than silently swallowing it.
  PERFORM _wf_seed_one(t_id, 'member_bosa_exit',
    'Member BOSA exit', 'Board — closes a BOSA-only member account and triggers the exit refund JE',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":72,"condition":null}
    ]$j$::jsonb);

END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one(uuid, text, text, text, jsonb);
