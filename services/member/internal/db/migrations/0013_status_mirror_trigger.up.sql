-- Phase E C: status mirror trigger. Today every status writer
-- (MemberStore.SetStatus, OrgMemberStore.SetStatus, StatusChangeStore.ApplyTx,
-- the app-approval activate flow that flips members.status='active')
-- has to remember to also update counterparties.status — and currently
-- no Go code does, so counterparties.status would drift the moment any
-- of those callers updates members/org_members.
--
-- The cross-check on tujenge shows 0 drift today because most rows are
-- still in their initial status (pending → active via app approval,
-- which sets counterparties.status correctly at creation). But the
-- invariant is fragile. A trigger holds it cheaply: AFTER UPDATE of
-- status on members or org_members, propagate to the bridged
-- counterparty row.
--
-- The counterparty enum is a superset of both member_status and
-- org_status (it covers pending|active|dormant|suspended|blacklisted|
-- exited|deceased|rejected). Casts use ::text intermediary so the
-- enum types don't have to match by-value at the type level.

CREATE OR REPLACE FUNCTION mirror_status_to_counterparty()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  -- Only fire when status actually changed (the AFTER UPDATE OF clause
  -- below filters to rows where status appeared in the SET list; this
  -- extra guard avoids self-no-op UPDATEs that touch status but keep
  -- the same value).
  IF NEW.status::text IS DISTINCT FROM OLD.status::text THEN
    UPDATE counterparties
       SET status = NEW.status::text::counterparty_status
     WHERE id = NEW.counterparty_id
       AND status::text <> NEW.status::text;
  END IF;
  RETURN NULL;
END;
$$;

CREATE TRIGGER trg_members_mirror_status_to_counterparty
  AFTER UPDATE OF status ON members
  FOR EACH ROW
  WHEN (NEW.counterparty_id IS NOT NULL)
  EXECUTE FUNCTION mirror_status_to_counterparty();

CREATE TRIGGER trg_org_members_mirror_status_to_counterparty
  AFTER UPDATE OF status ON org_members
  FOR EACH ROW
  WHEN (NEW.counterparty_id IS NOT NULL)
  EXECUTE FUNCTION mirror_status_to_counterparty();
