-- Phase E C: drop the legacy materialized_member_id +
-- materialized_org_id columns from membership_applications.
-- materialized_counterparty_id (added in member migration 0010) has
-- been the canonical post-approval bridge since Phase D shipped. The
-- two legacy columns were still being populated for backwards
-- compatibility but nothing reads them anymore.
--
-- Down migration re-adds the columns + backfills from the legacy
-- target on the counterparty row (cp.legacy_target_id is a uuid that
-- joins to either members.id or org_members.id depending on cp.kind).

BEGIN;

ALTER TABLE membership_applications DROP COLUMN IF EXISTS materialized_member_id;
ALTER TABLE membership_applications DROP COLUMN IF EXISTS materialized_org_id;

COMMIT;
