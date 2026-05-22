-- Phase D drop — the unified_counterparties flag served as a per-
-- tenant kill-switch during the Phase A/B/C rollout. With the
-- legacy detail + register pages deleted from the frontend and the
-- unified path now the only one, the flag has nothing to switch
-- between.
--
-- Reversible only by reintroducing the column AND re-adding the
-- flag-off branches in the FE; given those branches are gone with
-- this PR, treat this drop as one-way.

ALTER TABLE tenant_operations DROP COLUMN IF EXISTS unified_counterparties;
