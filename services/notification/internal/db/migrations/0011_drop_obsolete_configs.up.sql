-- Stage 9 follow-up: drop the per-tenant SMTP/SMS provider config
-- tables. They've been unused since the workers switched to the
-- shared platform_smtp_config / platform_sms_config singletons in
-- migration 0009. The handler/store code that read them was deleted
-- at the same time, so nothing references these rows any more.
--
-- Kept the data live for two migrations as a safety window in case
-- we needed to roll back the worker switch — that hasn't happened,
-- so drop now.

DROP TABLE IF EXISTS notification_sms_configs;
DROP TABLE IF EXISTS notification_smtp_configs;
