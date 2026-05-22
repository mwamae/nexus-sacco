-- ═══════════════════════════════════════════════════════════════════
-- Remove share-redemption support from the approval toggles.
--
-- Per the SACCO's policy: share capital is equity and cannot be
-- bought back by the cooperative. An exiting member must transfer
-- their shares to another active member; redemption is not a legal
-- operation. The 'approval_share_redeem' toggle on tenant_operations
-- (added in 0011_share_approvals) is therefore obsolete.
--
-- The 'shares:redeem' permission row is also dropped from the
-- identity service in a parallel migration.
-- ═══════════════════════════════════════════════════════════════════

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS approval_share_redeem;
