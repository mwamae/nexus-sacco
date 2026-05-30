-- DSID Phase 2.2 — workflow seed: deposit_account_reactivation.
--
-- Branch manager any-one approver, 48h SLA. Matches the savings
-- POST /v1/deposit-accounts/{id}/reactivate handler.

BEGIN;

CREATE OR REPLACE FUNCTION _wf_seed_one_v2(
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

DO $$
DECLARE
  t_id uuid;
BEGIN
FOR t_id IN SELECT id FROM tenants LOOP
  PERFORM _wf_seed_one_v2(t_id, 'deposit_account_reactivation',
    'Dormant account reactivation',
    'Reactivate a dormant deposit account after KYC refresh.',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);

  -- Also seed standing_order_resume — used when an officer resumes a
  -- standing order that was suspended within the last 7 days.
  PERFORM _wf_seed_one_v2(t_id, 'standing_order_resume',
    'Standing order resume (recent suspend)',
    'Branch manager approval before resuming a standing order suspended within the last 7 days.',
    $j$[
      {"name":"Reviewer","roles":["branch_manager"],"quorum":"any_one","sla_hours":48,"condition":null}
    ]$j$::jsonb);
END LOOP;
END $$;

DROP FUNCTION IF EXISTS _wf_seed_one_v2(uuid, text, text, text, jsonb);

COMMIT;
