-- counterparty_status_counts — canonical roll-call counts across BOTH
-- individual + institutional counterparties, with the same filter
-- dimensions the directory UI exposes.
--
-- Why this exists: the legacy member_status_counts(uuid) (migration
-- 0006) reads from the `members` table and so silently excludes
-- institutional counterparties (chamas / NGOs / companies / churches /
-- schools). The Members register page also displays institutions, so
-- the dashboard widget and the page header drifted any time an
-- institution was on register. This function takes counterparties as
-- its source — RLS-scoped, single-table — and accepts the same kind /
-- status / search filters the directory list endpoint accepts so the
-- two views can be wired to the same source and never disagree again.
--
-- Bucket semantics (inherited verbatim from 0006_member_status_counts;
-- see that migration's header for the long-form explanation):
--   total_on_register      = active + dormant + pending + suspended + blacklisted
--   total_active_servicing = active + dormant
-- Additive new buckets:
--   total_directory        = every counterparty regardless of status (mirrors
--                            what the register page actually displays
--                            before any status chip is selected)
--   individuals            = total_directory filtered to kind=individual
--   institutions           = total_directory filtered to kind<>individual
--
-- Filter semantics:
--   p_kind   'all' | 'individual' | 'institutional' (default 'all').
--            'institutional' = every kind except 'individual'.
--   p_status text[] — when NULL, no status filter. When non-NULL,
--            counterparties.status::text must appear in the array.
--   p_q      ILIKE match against display_name / cp_number / legacy_id.
--            NULL or '' bypasses the search predicate.
--
-- Filter predicates intentionally mirror the dynamic SQL in
-- CounterpartyStore.ListTx so the two sources can't drift. If you
-- ever extend ListTx's filter set, extend this function in lockstep
-- AND update the assertion sentinel in CounterpartyHandler.CountsV2.
--
-- Security: SECURITY INVOKER + explicit `WHERE tenant_id = p_tenant_id`.
-- RLS on counterparties will also constrain rows when called from a
-- tenant-scoped tx; the explicit predicate keeps the function correct
-- in maintenance / migration contexts where current_tenant_id() isn't set.

CREATE OR REPLACE FUNCTION counterparty_status_counts(
  p_tenant_id   uuid,
  p_kind        text   DEFAULT 'all',
  p_status      text[] DEFAULT NULL,
  p_q           text   DEFAULT NULL
)
RETURNS TABLE (
  active                  int,
  dormant                 int,
  pending                 int,
  suspended               int,
  blacklisted             int,
  exited                  int,
  deceased                int,
  rejected                int,
  total_on_register       int,
  total_active_servicing  int,
  total_directory         int,
  individuals             int,
  institutions            int
)
LANGUAGE sql
STABLE
AS $$
  WITH base AS (
    SELECT c.id, c.kind, c.status::text AS s
      FROM counterparties c
     WHERE c.tenant_id = p_tenant_id
       AND (p_kind = 'all'
            OR (p_kind = 'individual'    AND c.kind  = 'individual')
            OR (p_kind = 'institutional' AND c.kind <> 'individual'))
       AND (p_status IS NULL OR c.status::text = ANY (p_status))
       AND (p_q IS NULL OR p_q = ''
            OR c.display_name ILIKE '%' || p_q || '%'
            OR c.cp_number    ILIKE '%' || p_q || '%'
            OR COALESCE(c.legacy_id, '') ILIKE '%' || p_q || '%')
  ),
  counts AS (
    SELECT s, count(*)::int AS n FROM base GROUP BY s
  ),
  buckets AS (
    SELECT
      COALESCE((SELECT n FROM counts WHERE s = 'active'),      0) AS active,
      COALESCE((SELECT n FROM counts WHERE s = 'dormant'),     0) AS dormant,
      COALESCE((SELECT n FROM counts WHERE s = 'pending'),     0) AS pending,
      COALESCE((SELECT n FROM counts WHERE s = 'suspended'),   0) AS suspended,
      COALESCE((SELECT n FROM counts WHERE s = 'blacklisted'), 0) AS blacklisted,
      COALESCE((SELECT n FROM counts WHERE s = 'exited'),      0) AS exited,
      COALESCE((SELECT n FROM counts WHERE s = 'deceased'),    0) AS deceased,
      COALESCE((SELECT n FROM counts WHERE s = 'rejected'),    0) AS rejected,
      (SELECT count(*)::int FROM base) AS total_directory,
      (SELECT count(*)::int FROM base WHERE kind  = 'individual') AS individuals,
      (SELECT count(*)::int FROM base WHERE kind <> 'individual') AS institutions
  )
  SELECT
    active, dormant, pending, suspended, blacklisted, exited, deceased, rejected,
    (active + dormant + pending + suspended + blacklisted) AS total_on_register,
    (active + dormant)                                     AS total_active_servicing,
    total_directory, individuals, institutions
    FROM buckets;
$$;

COMMENT ON FUNCTION counterparty_status_counts(uuid, text, text[], text) IS
  'Canonical counterparty roll-call counts. Reads from counterparties to include both individual members and institutions. Supersedes member_status_counts(uuid) but additive — existing callers still compile. See migration 0006 + this file (0022) for bucket semantics; filter predicates mirror CounterpartyStore.ListTx so the directory page''s counts and the dashboard widget can''t drift.';

-- Forward-compat note: member_status_counts(uuid) is kept alive for
-- back-compat callers (status_handler tests, external dashboards).
-- Its header now points at this function as canonical.
COMMENT ON FUNCTION member_status_counts(uuid) IS
  'LEGACY — members-only roll-call counts. Kept for back-compat; new callers MUST use counterparty_status_counts(uuid, text, text[], text) from migration 0022 instead so institutional counterparties are not silently excluded. See 0006 header for bucket semantics.';
