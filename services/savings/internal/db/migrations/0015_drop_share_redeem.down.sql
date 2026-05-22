ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS approval_share_redeem boolean NOT NULL DEFAULT false;
