-- Preamble for Phase D sub-PR 2 — bridge notification_preferences,
-- the last notification-owned table that keys off members.id. Same
-- playbook as the savings/member preambles.
--
-- notification_preferences holds 0 live rows today, so the backfill
-- is a no-op in practice. The column + trigger still get added
-- so the destructive PR can drop member_id uniformly.
--
-- The trigger function is declared CREATE OR REPLACE so this
-- migration is safe to apply in any cross-service ordering.

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

ALTER TABLE notification_preferences
  ADD COLUMN counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX notification_preferences_counterparty_idx
  ON notification_preferences(counterparty_id)
  WHERE counterparty_id IS NOT NULL;
UPDATE notification_preferences np
   SET counterparty_id = m.counterparty_id
  FROM members m
 WHERE np.member_id = m.id
   AND m.counterparty_id IS NOT NULL;
CREATE TRIGGER trg_notification_preferences_populate_counterparty
  BEFORE INSERT ON notification_preferences
  FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member();
