-- Per-tenant feature flag gating the Unified Inbox consolidation
-- (PR #3 of the /approvals roll-up). When true, the savings
-- /cash-approvals queue renders read-only with a deprecation banner
-- and the canonical decision surface is /approvals (workflow
-- engine instances). Default false so every existing tenant keeps
-- the old behaviour until explicitly opted in.

ALTER TABLE tenants
  ADD COLUMN IF NOT EXISTS unified_inbox_enabled boolean NOT NULL DEFAULT false;
