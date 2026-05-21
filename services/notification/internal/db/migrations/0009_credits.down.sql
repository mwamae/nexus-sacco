-- Reverse Stage 9 (credit refactor). Drops every new object created
-- by 0009; the existing per-tenant SMTP/SMS config tables are
-- untouched (they pre-date 0009).

ALTER TABLE notification_deliveries DROP COLUMN IF EXISTS blocked_reason;

DROP TABLE IF EXISTS platform_sms_config;
DROP TABLE IF EXISTS platform_smtp_config;

DROP TABLE IF EXISTS notification_credit_adjustments;
DROP TABLE IF EXISTS notification_credit_pricing;
DROP TABLE IF EXISTS notification_credit_topup_requests;
DROP TABLE IF EXISTS notification_credit_ledger;
DROP TABLE IF EXISTS notification_credit_balances;

DROP TYPE IF EXISTS notification_adjustment_status;
DROP TYPE IF EXISTS notification_topup_status;
DROP TYPE IF EXISTS notification_credit_movement;
