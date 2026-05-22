-- Per-tenant feature flag controlling whether the unified
-- counterparties register is active for reads + UI surfaces.
--
-- Off: /members + /orgs + their UI behave exactly as they did before
-- migration 0007. Mirror writes to counterparties run regardless so
-- a tenant can flip the flag at any time with no data gap.
--
-- On: list / detail / search use the counterparties store; legacy
-- endpoints become thin wrappers; the admin UI shows the Kind column
-- and unified register.
--
-- See docs/unified-counterparty-merge.md for the full rollout plan.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS unified_counterparties boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN tenant_operations.unified_counterparties IS
  'Phase B feature flag for the counterparties unification. Default off; flip per-tenant once the Phase A backfill (member service 0008) has run and been verified.';
