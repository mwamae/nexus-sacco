-- Seed the 19 new process_kind workflow definitions for every existing
-- tenant. Idempotent — uses WHERE NOT EXISTS guards so re-running is a
-- no-op (the partial unique index on (tenant_id, process_kind) WHERE
-- active=true can't be the target of ON CONFLICT).
--
-- NOT touched (already in use by other modules):
--   • loan_disbursement
--   • member_onboarding
--   • member_status_change
--
-- Role mapping (from services/identity .../0002_seed_rbac.up.sql):
--   "Checker"        → branch_manager
--   "Reviewer"       → branch_manager
--   "Credit officer" → credit_officer
--   "Approver"       → sacco_admin
--   "Board"          → [sacco_admin, tenant_owner]
--
-- Tenants can edit any of these in /workflows after migration —
-- versioning is handled by the engine's standard "new version bumps
-- and deactivates the old one" flow.

-- A function-style helper for the repetitive (defs INSERT + level
-- INSERTs) pattern. Defined inline so we can DROP it at the end and
-- not pollute the schema.
CREATE OR REPLACE FUNCTION _wf_seed_one(
  p_tenant_id      uuid,
  p_process_kind   text,
  p_name           text,
  p_description    text,
  p_levels         jsonb   -- array of { name, roles[], quorum, sla_hours, condition }
) RETURNS void AS $$
DECLARE
  v_def_id uuid;
  v_level  jsonb;
  v_order  int := 0;
BEGIN
  -- Skip if an active definition already exists for this (tenant, kind).
  SELECT id INTO v_def_id
    FROM wf_definitions
   WHERE tenant_id = p_tenant_id
     AND process_kind = p_process_kind
     AND active = true
   LIMIT 1;
  IF v_def_id IS NOT NULL THEN
    RETURN;  -- already seeded; leave alone
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
      v_level->'condition',                       -- may be NULL
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

  -- ── Cash money movements ──────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'cash_deposit',
    'Cash deposit', 'Maker-checker on a cash deposit',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":4,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'cash_withdrawal',
    'Cash withdrawal', 'Checker for every withdrawal; Board approval over KES 500,000',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":4,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":4,
       "condition":{">":[{"var":"amount"},500000]}}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'cash_account_transfer',
    'Cash account transfer', 'Maker-checker on an internal account transfer',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":4,"condition":null}
    ]$j$::jsonb);

  -- ── Shares ────────────────────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'share_purchase',
    'Share purchase', 'Maker-checker on a share purchase',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'share_transfer',
    'Share transfer', 'Checker then Board for share transfers',
    $j$[
      {"name":"Checker","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'share_bonus_issue',
    'Share bonus issue', 'Board only — requires both SACCO Admin AND Tenant Owner',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"all","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  -- ── Loans ─────────────────────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'loan_application_decision',
    'Loan application credit decision', 'Three-eye credit decision: Credit Officer → Reviewer → Approver',
    $j$[
      {"name":"Credit Officer","roles":["credit_officer"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Approver","roles":["sacco_admin"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_reschedule',
    'Loan reschedule', 'Credit officer → Reviewer → Board',
    $j$[
      {"name":"Credit Officer","roles":["credit_officer"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_moratorium',
    'Loan moratorium', 'Credit officer → Reviewer → Board',
    $j$[
      {"name":"Credit Officer","roles":["credit_officer"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_write_off',
    'Loan write-off', 'Board only — requires both SACCO Admin AND Tenant Owner',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"all","sla_hours":72,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'loan_settlement_discount',
    'Loan settlement discount', 'Reviewer → Board',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  -- ── Member lifecycle (variants — onboarding/status_change are pre-existing) ─
  PERFORM _wf_seed_one(t_id, 'member_blacklist',
    'Member blacklist', 'Reviewer → Board',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'member_close',
    'Member close', 'Reviewer → Board',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'member_reactivate',
    'Member reactivate', 'Reviewer only',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

  -- ── Batch / periodic ──────────────────────────────────────────
  PERFORM _wf_seed_one(t_id, 'bulk_dormancy_run',
    'Bulk dormancy detector', 'Reviewer → Board approval of the full batch result',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'interest_run',
    'Interest run', 'Board sign-off before posting',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'dividend_run',
    'Dividend run', 'Board sign-off before posting',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'year_end_close',
    'Year-end close', 'Board sign-off — requires both SACCO Admin AND Tenant Owner',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"all","sla_hours":72,"condition":null}
    ]$j$::jsonb);

  -- ── Accounting / destructive ──────────────────────────────────
  -- Manual journal entries: only require approval when amount > 100k
  -- OR the entry touches equity. Both levels carry the same gating
  -- condition so below-threshold entries skip both and the engine
  -- auto-approves at creation (per the existing all-skipped path).
  PERFORM _wf_seed_one(t_id, 'manual_journal_entry',
    'Manual journal entry', 'Reviewer → Board, but only if amount > KES 100k or equity-affecting',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":24,
       "condition":{"or":[{">":[{"var":"amount"},100000]},{"==":[{"var":"affects_equity"},true]}]}},
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":24,
       "condition":{"or":[{">":[{"var":"amount"},100000]},{"==":[{"var":"affects_equity"},true]}]}}
    ]$j$::jsonb);

  PERFORM _wf_seed_one(t_id, 'journal_reversal',
    'Journal reversal', 'Always Board — reversals never auto-execute',
    $j$[
      {"name":"Board","roles":["sacco_admin","tenant_owner"],"quorum":"any_one","sla_hours":24,"condition":null}
    ]$j$::jsonb);

END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one(uuid, text, text, text, jsonb);
