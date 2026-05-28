DROP VIEW IF EXISTS tenant_approval_toggles;

-- Restore the original column comments (best-effort; reverts the
-- DEPRECATED markers added by the up migration).
COMMENT ON COLUMN tenant_operations.approval_deposit IS NULL;
COMMENT ON COLUMN tenant_operations.approval_withdrawal IS NULL;
COMMENT ON COLUMN tenant_operations.approval_deposit_transfer IS NULL;
