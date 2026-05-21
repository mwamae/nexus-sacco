-- Stage 2 of the maker-checker rollout — share actions.
--
-- Adds per-kind approval toggles for share purchase, redeem, transfer,
-- bonus issue, and lien placement. Each follows the same pattern as
-- the deposit toggles wired in 0010_pending_approvals.up.sql.

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS approval_share_purchase boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_share_redeem   boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_share_transfer boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_share_bonus    boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_share_lien     boolean NOT NULL DEFAULT false;
