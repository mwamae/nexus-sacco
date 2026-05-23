-- Preamble for Phase D sub-PR 2 — bridge the four member-owned
-- tables that still key off members.id without a matching
-- counterparty_id column. Same playbook as savings migration 0018:
-- additive uuid column, partial index, one-shot backfill, BEFORE
-- INSERT trigger.
--
-- Covered tables (and why each matters):
--   member_documents       (7 rows) — uploaded KYC documents
--   member_relations       (7 rows) — next-of-kin / dependent links
--   member_status_changes  (6 rows) — audit log of status transitions
--   member_status_proposals (5 rows) — pending status proposals
--
-- The trigger function is created CREATE OR REPLACE for
-- cross-service safety: the savings service's migration 0018 owns
-- the canonical definition, but member migrations may apply first
-- in some deploy orderings. The body is intentionally identical to
-- savings/0018's so the two never drift.

CREATE OR REPLACE FUNCTION populate_counterparty_id_from_member()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.counterparty_id IS NULL AND NEW.member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.counterparty_id
      FROM members WHERE id = NEW.member_id;
  END IF;
  RETURN NEW;
END;
$$;

ALTER TABLE member_documents
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX member_documents_counterparty_idx
  ON member_documents(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE member_documents md
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE md.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_member_documents_populate_counterparty
  BEFORE INSERT ON member_documents
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_relations
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX member_relations_counterparty_idx
  ON member_relations(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE member_relations mr
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE mr.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_member_relations_populate_counterparty
  BEFORE INSERT ON member_relations
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_status_changes
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX member_status_changes_counterparty_idx
  ON member_status_changes(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE member_status_changes msc
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE msc.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_member_status_changes_populate_counterparty
  BEFORE INSERT ON member_status_changes
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_status_proposals
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX member_status_proposals_counterparty_idx
  ON member_status_proposals(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE member_status_proposals msp
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE msp.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_member_status_proposals_populate_counterparty
  BEFORE INSERT ON member_status_proposals
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
