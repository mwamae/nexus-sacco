DROP TABLE IF EXISTS loan_recoveries;
DROP TABLE IF EXISTS loan_writeoffs;
ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS provisioning_loss_pct,
  DROP COLUMN IF EXISTS provisioning_doubtful_pct,
  DROP COLUMN IF EXISTS provisioning_substandard_pct,
  DROP COLUMN IF EXISTS provisioning_watch_pct;
