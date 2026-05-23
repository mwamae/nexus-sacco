DROP TRIGGER IF EXISTS trg_notification_preferences_populate_counterparty ON notification_preferences;
DROP INDEX IF EXISTS notification_preferences_counterparty_idx;
ALTER TABLE notification_preferences DROP COLUMN IF EXISTS counterparty_id;

-- populate_counterparty_id_from_member is shared; do not drop here.
