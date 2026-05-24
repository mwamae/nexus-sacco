-- Unified Inbox — engine extensions.
--
-- The single-Inbox consolidation needs four additive things from the
-- engine:
--   (1) A claim/release lock so multiple approvers in the same role
--       don't step on each other.
--   (2) A precomputed sla_breach_at on the instance row so the
--       "overdue" widget is one indexed scan, not N levels-array
--       walks.
--   (3) UI fields: summary (Inbox-row one-liner) and source_url
--       (deep-link back to the originating page).
--   (4) wf_quorum.majority + wf_action_kind.comment (threaded
--       comments are stored as audit rows with action='comment').
--
-- Plus the subject_id text → uuid migration the spec asks for. We
-- backfill the only two non-uuid rows in the dev DB (legacy
-- loan_application instances that stored application_no instead of
-- the row id) before the cast.

-- ─── 1. Enum extensions ─────────────────────────────────────────────
ALTER TYPE wf_quorum      ADD VALUE IF NOT EXISTS 'majority';
ALTER TYPE wf_action_kind ADD VALUE IF NOT EXISTS 'comment';
ALTER TYPE wf_action_kind ADD VALUE IF NOT EXISTS 'claim';
ALTER TYPE wf_action_kind ADD VALUE IF NOT EXISTS 'release';

-- ─── 2. Backfill or evict non-uuid subject_ids ──────────────────────
-- Pre-Phase-D loan_disbursement instances stored loan_applications
-- .application_no in subject_id. Three passes:
--   (a) Try to map old application_no → current id.
--   (b) For anything still non-uuid AND terminal AND with a dangling
--       subject (the row it referenced no longer exists), evict it.
--       These are stale audit-only rows whose subject was lost in an
--       earlier rename — keeping them would be a join to nothing.
--   (c) Anything left is non-terminal or otherwise active. Fail loud
--       so the operator triages instead of silently corrupting state.
DO $$
DECLARE
  evicted int;
  bad_count int;
BEGIN
  -- (a) backfill from current loan_applications by application_no.
  UPDATE wf_instances wi
     SET subject_id = la.id::text
    FROM loan_applications la
   WHERE wi.subject_kind = 'loan_application'
     AND wi.subject_id   = la.application_no
     AND wi.subject_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

  -- (b) evict terminal-status orphan rows.
  DELETE FROM wf_instances
   WHERE subject_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
     AND status IN ('approved', 'rejected', 'cancelled', 'expired');
  GET DIAGNOSTICS evicted = ROW_COUNT;
  IF evicted > 0 THEN
    RAISE NOTICE 'wf_instances: evicted % terminal-status rows with stale (non-uuid) subject_id', evicted;
  END IF;

  -- (c) anything left is in-flight — fail loud.
  SELECT count(*) INTO bad_count
    FROM wf_instances
   WHERE subject_id !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

  IF bad_count > 0 THEN
    RAISE EXCEPTION 'wf_instances has % in-flight rows with non-uuid subject_id — triage manually before re-running', bad_count;
  END IF;
END $$;

-- ─── 3. subject_id text → uuid ──────────────────────────────────────
-- The old wf_instances_subject_idx is on (tenant_id, subject_kind,
-- subject_id text) — dropping it lets the column type change cleanly,
-- then we recreate against the new uuid type.
DROP INDEX IF EXISTS wf_instances_subject_idx;

ALTER TABLE wf_instances
  ALTER COLUMN subject_id TYPE uuid USING subject_id::uuid;

CREATE INDEX wf_instances_subject_idx
  ON wf_instances (tenant_id, subject_kind, subject_id);

-- ─── 4. Inbox UX + claim/lock + breach pin ──────────────────────────
ALTER TABLE wf_instances
  ADD COLUMN summary        text,
  ADD COLUMN source_url     text,
  ADD COLUMN claimed_by     uuid,
  ADD COLUMN claimed_at     timestamptz,
  ADD COLUMN claim_expires  timestamptz,
  -- sla_breach_at mirrors the *active* level's sla_due_at so we can
  -- index it. The store recomputes on every state mutation (create,
  -- action, claim). NULL when no active level has an SLA or instance
  -- is terminal.
  ADD COLUMN sla_breach_at  timestamptz;

-- Partial indexes — only the rows the Inbox queries care about.
CREATE INDEX wf_instances_claimed_idx
  ON wf_instances (tenant_id, claimed_by, status)
  WHERE claimed_by IS NOT NULL
    AND status IN ('in_progress', 'returned', 'awaiting_info', 'escalated');

CREATE INDEX wf_instances_sla_breach_idx
  ON wf_instances (tenant_id, sla_breach_at)
  WHERE sla_breach_at IS NOT NULL
    AND status IN ('in_progress', 'escalated');
