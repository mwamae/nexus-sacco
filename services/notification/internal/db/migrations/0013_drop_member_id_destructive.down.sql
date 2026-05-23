CREATE OR REPLACE FUNCTION populate_counterparty_id_from_member()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.counterparty_id IS NULL AND NEW.member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.counterparty_id
      FROM members WHERE id = NEW.member_id;
  END IF;
  RETURN NEW;
END; $$;

ALTER TABLE notification_preferences ADD COLUMN member_id uuid REFERENCES members(id) ON DELETE RESTRICT;
UPDATE notification_preferences np SET member_id = m.id FROM members m WHERE m.counterparty_id = np.counterparty_id;
ALTER TABLE notification_preferences ALTER COLUMN counterparty_id DROP NOT NULL;
CREATE UNIQUE INDEX notification_preferences_tenant_id_member_id_key ON notification_preferences(tenant_id, member_id);
CREATE TRIGGER trg_notification_preferences_populate_counterparty BEFORE INSERT ON notification_preferences FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
