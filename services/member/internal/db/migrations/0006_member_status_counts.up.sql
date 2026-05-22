-- member_status_counts(tenant_id) — canonical, single source of truth
-- for member roll-call numbers.
--
-- Why this exists: the admin dashboard widget and the Members page KPI
-- strip used to compute totals independently (dashboard summed every
-- status bucket including 'exited'/'deceased'; Members page silently
-- absorbed non-{active,pending,rejected} members into a generic "total"
-- line). The two views disagreed. This function freezes the bucket
-- semantics in one place so both views can never drift apart.
--
-- ─────────── Bucket semantics (confirmed 2026-05-22) ───────────
--   total_on_register      = active + dormant + pending + suspended + blacklisted
--     - Blacklisted: IN. They're barred from operations but still on the
--       books for legal / regulatory reporting.
--     - exited / deceased: OUT. They have left the register and should
--       not inflate any roll-call total.
--     - rejected: OUT. The application was declined; they were never on
--       the register.
--
--   total_active_servicing = active + dormant
--     - Dormant is rolled into "active" because dormancy is an
--       inactivity-driven UI/risk filter, not a deregistration. A
--       dormant member still has money on the books and is recoverable
--       via reactivation. Board / regulator reports should see them as
--       members.
--
--   Per-status fields (active, dormant, pending, suspended, blacklisted,
--   exited, deceased, rejected) are returned alongside the aggregates so
--   callers can drill in without re-issuing the count query.
--
-- ─────────── Security ───────────
--   SECURITY INVOKER + explicit `WHERE tenant_id = p_tenant_id`. RLS on
--   `members` will also constrain the rows when called from a
--   tenant-scoped transaction; the explicit predicate is the contract
--   and keeps the function correct in maintenance / migration contexts
--   where `current_tenant_id()` may not be set.

CREATE OR REPLACE FUNCTION member_status_counts(p_tenant_id uuid)
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
  total_active_servicing  int
)
LANGUAGE sql
STABLE
AS $$
  WITH counts AS (
    SELECT status::text AS s, count(*)::int AS n
      FROM members
     WHERE tenant_id = p_tenant_id
     GROUP BY status
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
      COALESCE((SELECT n FROM counts WHERE s = 'rejected'),    0) AS rejected
  )
  SELECT
    active, dormant, pending, suspended, blacklisted, exited, deceased, rejected,
    (active + dormant + pending + suspended + blacklisted) AS total_on_register,
    (active + dormant)                                     AS total_active_servicing
    FROM buckets;
$$;

COMMENT ON FUNCTION member_status_counts(uuid) IS
  'Canonical member roll-call counts for a tenant. Single source of truth — both the admin dashboard widget and the Members page KPI strip consume this. See migration 0006 for full bucket-semantic documentation.';
