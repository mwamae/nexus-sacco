-- Replace the two per-kind materialised columns
-- (materialized_member_id from 0005, materialized_org_id from 0009)
-- with a single counterparty-shaped pointer: materialized_counterparty_id.
--
-- This migration is ADDITIVE: it adds the new column + backfills,
-- but leaves the old columns in place. A follow-up Phase D+ PR will
-- drop them once the activation pipeline + read paths have been
-- updated to consume materialized_counterparty_id exclusively.
--
-- Backfill rules:
--   * If materialized_member_id is set → use members.counterparty_id
--   * Else if materialized_org_id is set → use org_members.counterparty_id
--   * Else NULL (application not yet materialised)
-- The COALESCE handles both individual and institutional rows uniformly.

ALTER TABLE membership_applications
  ADD COLUMN IF NOT EXISTS materialized_counterparty_id uuid REFERENCES counterparties(id) ON DELETE SET NULL;

UPDATE membership_applications a
   SET materialized_counterparty_id = COALESCE(
         (SELECT m.counterparty_id FROM members      m WHERE m.id = a.materialized_member_id),
         (SELECT o.counterparty_id FROM org_members  o WHERE o.id = a.materialized_org_id)
       )
 WHERE materialized_counterparty_id IS NULL
   AND (materialized_member_id IS NOT NULL OR materialized_org_id IS NOT NULL);

CREATE INDEX IF NOT EXISTS applications_materialized_cp_idx
  ON membership_applications (materialized_counterparty_id)
  WHERE materialized_counterparty_id IS NOT NULL;

COMMENT ON COLUMN membership_applications.materialized_counterparty_id IS
  'Counterparty produced by approval of this application. Source of truth post-Phase D; the per-kind materialized_member_id / materialized_org_id columns are kept for one release as the rollback path and will be dropped in Phase D+.';
