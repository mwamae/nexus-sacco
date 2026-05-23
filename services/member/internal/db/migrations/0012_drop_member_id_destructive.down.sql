CREATE OR REPLACE FUNCTION populate_counterparty_id_from_member()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.counterparty_id IS NULL AND NEW.member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.counterparty_id
      FROM members WHERE id = NEW.member_id;
  END IF;
  RETURN NEW;
END; $$;

ALTER TABLE member_documents       ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE member_documents md       SET member_id = m.id FROM members m WHERE m.counterparty_id = md.counterparty_id;
ALTER TABLE member_documents       ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX member_documents_member_idx ON member_documents(member_id);
CREATE UNIQUE INDEX member_documents_member_id_kind_key ON member_documents(member_id, kind);
CREATE TRIGGER trg_member_documents_populate_counterparty BEFORE INSERT ON member_documents FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_relations       ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE member_relations mr       SET member_id = m.id FROM members m WHERE m.counterparty_id = mr.counterparty_id;
ALTER TABLE member_relations       ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX member_relations_member_idx ON member_relations(member_id, kind, "position");
CREATE TRIGGER trg_member_relations_populate_counterparty BEFORE INSERT ON member_relations FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_status_changes  ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE member_status_changes msc  SET member_id = m.id FROM members m WHERE m.counterparty_id = msc.counterparty_id;
ALTER TABLE member_status_changes  ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX member_status_changes_member_idx ON member_status_changes(member_id, changed_at DESC);
CREATE TRIGGER trg_member_status_changes_populate_counterparty BEFORE INSERT ON member_status_changes FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();

ALTER TABLE member_status_proposals ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE member_status_proposals msp SET member_id = m.id FROM members m WHERE m.counterparty_id = msp.counterparty_id;
ALTER TABLE member_status_proposals ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE INDEX member_status_proposals_member_idx ON member_status_proposals(member_id) WHERE (resolved_at IS NULL);
CREATE TRIGGER trg_member_status_proposals_populate_counterparty BEFORE INSERT ON member_status_proposals FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
