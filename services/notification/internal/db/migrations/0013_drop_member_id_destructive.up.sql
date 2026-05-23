BEGIN;
-- Phase D sub-PR 2a — drop notification_preferences.member_id. The
-- column was bridged by migration 0012 (preamble); counterparty_id is
-- now the canonical FK. notification_preferences has 0 live rows so
-- this is mostly cosmetic, but uniform cleanup matters for the
-- destructive PR's coherence.

DROP TRIGGER IF EXISTS trg_notification_preferences_populate_counterparty ON notification_preferences;
ALTER TABLE notification_preferences DROP CONSTRAINT IF EXISTS notification_preferences_tenant_id_member_id_key;
ALTER TABLE notification_preferences DROP COLUMN IF EXISTS member_id;
DROP INDEX IF EXISTS notification_preferences_counterparty_idx;
ALTER TABLE notification_preferences ALTER COLUMN counterparty_id SET NOT NULL;
CREATE INDEX notification_preferences_counterparty_idx
  ON notification_preferences(counterparty_id);
CREATE UNIQUE INDEX IF NOT EXISTS notification_preferences_tenant_id_counterparty_id_key
  ON notification_preferences(tenant_id, counterparty_id);

-- The savings service owns the trigger function and drops it (CASCADE)
-- in its own migration 0021. No-op here.

COMMIT;
