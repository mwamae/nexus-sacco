BEGIN;

ALTER TABLE tenant_operations DROP COLUMN IF EXISTS statement_mail_cron;
DROP TABLE IF EXISTS member_notification_preferences;
DROP TABLE IF EXISTS statement_mailings;

COMMIT;
