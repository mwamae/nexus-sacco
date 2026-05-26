-- counterparty_directory — read-only view over counterparties (+ the
-- per-kind details on members / org_members) so list queries can JOIN
-- it without having to handle the individual / institutional split.
--
-- Bug it closes: every list query in services/savings/internal/store
-- does `JOIN members m ON m.counterparty_id = a.counterparty_id`,
-- which silently excludes institutional accounts (chamas, companies,
-- NGOs, churches, schools — every kind that has an org_members row
-- but no members row). Shares + Deposits + Loans + Reports +
-- Collections + Guarantees all hide org rows; the subledger totals
-- diverge from the Balance Sheet by the institutional balance.
--
-- The view's contract:
--
--   counterparty_id  — the universal id; FKs from every transaction
--                      table point at this.
--   tenant_id        — RLS scoping; mirrored from counterparties.
--   kind             — counterparty_kind enum value
--                      (individual / chama / company / ngo / church /
--                      school / other).
--   cp_number        — the long-lived public identifier.
--   full_name        — display_name (works for both kinds; orgs use
--                      their registered_name, individuals their
--                      full_name).
--   cp_status        — counterparties.status::text (already mirrored
--                      from members.status / org_members.status via
--                      the trg_*_mirror_status_to_counterparty
--                      triggers — see migration 0007).
--   member_no        — COALESCE(members.member_no, org_members.org_no,
--                      cp_number) so the existing search predicate
--                      (q LIKE '%' || member_no || '%') keeps working
--                      for orgs.
--   is_institution   — derived: kind != 'individual'. UI uses this
--                      to render the "Org" chip + route to /orgs/.
--   member_id        — members.id (nullable; NULL on org rows). Kept
--                      for callers that need to bridge into the
--                      member-service domain.
--   org_id           — org_members.id (nullable; NULL on individual
--                      rows). Symmetric.
--
-- Idempotency: CREATE OR REPLACE. Drop in the .down.sql.
--
-- RLS: the view inherits the underlying tables' RLS — counterparties
-- has `tenant_id = current_tenant_id()` policy enforced, members +
-- org_members same. A query against this view from inside a
-- tenant-scoped tx returns only that tenant's rows.

-- security_invoker = true so the view runs as the querying role
-- (nexus_app at runtime), NOT as the view owner (nexus superuser).
-- Without this, RLS on counterparties/members/org_members would be
-- bypassed: a superuser-owned view defaults to running as the owner,
-- which inherits owner privileges (BYPASSRLS) and silently exposes
-- cross-tenant rows. PG 16+ required for this option.
CREATE OR REPLACE VIEW counterparty_directory
WITH (security_invoker = true) AS
SELECT
  c.id                                                       AS counterparty_id,
  c.tenant_id                                                AS tenant_id,
  c.kind                                                     AS kind,
  c.cp_number                                                AS cp_number,
  c.display_name                                             AS full_name,
  c.status::text                                             AS cp_status,
  COALESCE(m.member_no, om.org_no, c.cp_number)              AS member_no,
  (c.kind <> 'individual')                                   AS is_institution,
  m.id                                                       AS member_id,
  om.id                                                      AS org_id
FROM counterparties c
LEFT JOIN members      m  ON m.counterparty_id  = c.id
LEFT JOIN org_members  om ON om.counterparty_id = c.id;

-- Defensive ALTER in case the view existed prior with the default
-- security_invoker=false. CREATE OR REPLACE retains existing
-- options; ALTER explicitly flips it. Idempotent.
ALTER VIEW counterparty_directory SET (security_invoker = true);

COMMENT ON VIEW counterparty_directory IS
  'Universal identity for list queries. JOIN this instead of `members` so org accounts show up in shares/deposits/loans/reports. See migration 0037 for the contract.';
