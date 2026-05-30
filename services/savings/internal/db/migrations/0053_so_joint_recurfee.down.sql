BEGIN;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS joint_withdrawal_expiry_hours,
  DROP COLUMN IF EXISTS default_joint_required_signers,
  DROP COLUMN IF EXISTS standing_order_notify_on_suspend,
  DROP COLUMN IF EXISTS standing_order_notify_on_failure,
  DROP COLUMN IF EXISTS standing_order_suspend_after_failures,
  DROP COLUMN IF EXISTS standing_order_retry_backoff_hours,
  DROP COLUMN IF EXISTS standing_order_max_retries;

DROP TABLE IF EXISTS deposit_account_recurring_fee_charges;
DROP TABLE IF EXISTS deposit_product_recurring_fees;
DROP TYPE IF EXISTS recurring_fee_frequency;

DROP TABLE IF EXISTS joint_withdrawal_authorisations;
DROP TABLE IF EXISTS withdrawal_authorisations;
DROP TABLE IF EXISTS deposit_account_joint_owners;

ALTER TABLE deposit_accounts
  DROP COLUMN IF EXISTS required_signers,
  DROP COLUMN IF EXISTS is_joint;

DROP TABLE IF EXISTS recurring_deposit_runs;
DROP TABLE IF EXISTS recurring_deposits;
DROP TYPE IF EXISTS standing_order_frequency;
DROP TYPE IF EXISTS standing_order_status;
DROP TYPE IF EXISTS standing_order_source;

COMMIT;
