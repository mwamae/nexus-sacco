-- Mark tenants.unified_inbox_enabled deprecated.
--
-- The unified-approvals migration retired the per-tenant rollout
-- flag — every cash kind now routes through the workflow engine
-- unconditionally. The Go services pin tenantHasUnifiedInbox() to
-- true regardless of the column value (see
-- services/accounting/internal/handler/journal_workflow.go and
-- services/member/internal/handler/status.go), and the frontend
-- /v1/inbox-status endpoint hard-codes unified_inbox_enabled=true
-- in its response (services/workflow/internal/handler/inbox_status.go).
--
-- The column itself stays for one release as a panic-revert escape
-- hatch — if a critical bug surfaces in the workflow path for one
-- tenant, an operator can flip the column to false on that tenant
-- AND temporarily revert the hard-pin in tenantHasUnifiedInbox()
-- to recover. Next major release deletes the column AND every
-- now-dead legacy branch.

COMMENT ON COLUMN tenants.unified_inbox_enabled IS
  'DEPRECATED — every tenant uses the workflow inbox unconditionally as of the unified-approvals migration. Column retained for one release as a panic-revert escape; the per-service Go helpers are hard-pinned to true and ignore this value. Removed in the next major release together with the dead legacy code branches.';
