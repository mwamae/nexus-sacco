DROP TABLE IF EXISTS pending_approvals;
ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS approval_allow_self,
  DROP COLUMN IF EXISTS approval_deposit_transfer,
  DROP COLUMN IF EXISTS approval_withdrawal,
  DROP COLUMN IF EXISTS approval_deposit;
