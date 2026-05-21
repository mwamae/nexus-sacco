DROP INDEX IF EXISTS notification_deliveries_due_idx;
ALTER TABLE notification_deliveries
  DROP COLUMN IF EXISTS max_attempts,
  DROP COLUMN IF EXISTS next_retry_at;
DROP TABLE IF EXISTS notification_smtp_configs;
DELETE FROM notification_templates WHERE channel = 'email';
