-- Re-add the two legacy materialized_* columns. Best-effort backfill
-- from the counterparty bridge: for individual apps the matching
-- members.id is the row whose members.counterparty_id equals
-- materialized_counterparty_id; for institutional apps, the same join
-- to org_members. Rows that pre-date the counterparty bridge stay
-- NULL on rollback — the columns were nullable originally so that's
-- compatible with the upstream schema.

BEGIN;

ALTER TABLE membership_applications ADD COLUMN materialized_member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
ALTER TABLE membership_applications ADD COLUMN materialized_org_id    uuid REFERENCES org_members(id) ON DELETE RESTRICT;

UPDATE membership_applications a
   SET materialized_member_id = m.id
  FROM members m
 WHERE m.counterparty_id = a.materialized_counterparty_id
   AND a.kind = 'individual'::membership_application_kind;

UPDATE membership_applications a
   SET materialized_org_id = o.id
  FROM org_members o
 WHERE o.counterparty_id = a.materialized_counterparty_id
   AND a.kind = 'institutional'::membership_application_kind;

COMMIT;
