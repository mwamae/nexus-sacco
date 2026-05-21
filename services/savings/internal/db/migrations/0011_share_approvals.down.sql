ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS approval_share_lien,
  DROP COLUMN IF EXISTS approval_share_bonus,
  DROP COLUMN IF EXISTS approval_share_transfer,
  DROP COLUMN IF EXISTS approval_share_redeem,
  DROP COLUMN IF EXISTS approval_share_purchase;
