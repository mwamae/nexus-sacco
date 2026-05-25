-- Approval-coverage completion + safe defaults + audit.
--
-- Two correctness goals + one new feature:
--
--   1. Close the two previously-uncovered approval-toggle paths
--      with three new flags on tenant_operations:
--        • approval_fee_collection     — Collection Desk fee lines
--        • approval_welfare_collection — Collection Desk welfare lines
--        • approval_application_fee    — late-capture registration fees
--      Defaults are TRUE so a fresh tenant ships safely.
--
--   2. Flip every existing approval_* column's DEFAULT from FALSE
--      → TRUE (except approval_allow_self, which stays FALSE for
--      sane segregation of duties). Then back-flip every existing
--      tenant row that wasn't already fully gated, and audit each
--      flip into tenant_approval_changes so any opt-out is visible.
--
--   3. Add the tenant_approval_changes append-only audit table.
--      Every toggle flip from now on lands a row here, written in
--      the same tx as the toggle update. An UPDATE/DELETE trigger
--      enforces append-only semantics.

-- ─────────── A. Audit table — must exist BEFORE the backfill ───────────
-- The spec's snippet ordered this AFTER the UPDATE+INSERT, which
-- would have failed (you can't INSERT into a table that doesn't
-- exist yet). Creating first.
CREATE TABLE IF NOT EXISTS tenant_approval_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- Plain uuid (no FK to users) to match the convention used by
  -- pending_approvals and the existing audit_log table. NULL means
  -- the row was written by the migration or another system path.
  changed_by_user_id uuid,
  field              text NOT NULL,
  old_value          text,
  new_value          text NOT NULL,
  -- Required at the handler layer when new_value='false' (opt-out
  -- relaxes a gate); optional otherwise. The CHECK below is a
  -- belt-and-braces guard at the DB layer.
  reason             text,
  changed_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (new_value <> 'false' OR reason IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS tenant_approval_changes_tenant_idx
  ON tenant_approval_changes (tenant_id, changed_at DESC);

ALTER TABLE tenant_approval_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_approval_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_tenant_approval_changes ON tenant_approval_changes
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON tenant_approval_changes TO nexus_app;
-- Deliberately NOT granting UPDATE / DELETE. The trigger below
-- also blocks them, but defense in depth.

-- Append-only trigger. Blocks any UPDATE / DELETE against the
-- table. Migrations themselves bypass triggers when run by the
-- superuser, so a down migration can still drop the table.
CREATE OR REPLACE FUNCTION tenant_approval_changes_no_mutate() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'tenant_approval_changes is append-only (op %)', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tenant_approval_changes_no_update ON tenant_approval_changes;
DROP TRIGGER IF EXISTS tenant_approval_changes_no_delete ON tenant_approval_changes;
CREATE TRIGGER tenant_approval_changes_no_update
  BEFORE UPDATE ON tenant_approval_changes
  FOR EACH ROW EXECUTE FUNCTION tenant_approval_changes_no_mutate();
CREATE TRIGGER tenant_approval_changes_no_delete
  BEFORE DELETE ON tenant_approval_changes
  FOR EACH ROW EXECUTE FUNCTION tenant_approval_changes_no_mutate();

-- ─────────── B. New toggles (defaults TRUE) ───────────
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS approval_fee_collection     boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS approval_welfare_collection boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS approval_application_fee    boolean NOT NULL DEFAULT true;

-- ─────────── C. Flip DEFAULT true on the existing 15 toggles ───────────
-- This only affects future inserts; existing rows are handled by
-- the backfill below. approval_allow_self stays DEFAULT false.
ALTER TABLE tenant_operations
  ALTER COLUMN approval_deposit                  SET DEFAULT true,
  ALTER COLUMN approval_withdrawal               SET DEFAULT true,
  ALTER COLUMN approval_deposit_transfer         SET DEFAULT true,
  ALTER COLUMN approval_share_purchase           SET DEFAULT true,
  ALTER COLUMN approval_share_transfer           SET DEFAULT true,
  ALTER COLUMN approval_share_bonus              SET DEFAULT true,
  ALTER COLUMN approval_share_lien               SET DEFAULT true,
  ALTER COLUMN approval_loan_disbursement        SET DEFAULT true,
  ALTER COLUMN approval_loan_repayment           SET DEFAULT true,
  ALTER COLUMN approval_loan_settle              SET DEFAULT true,
  ALTER COLUMN approval_loan_reverse             SET DEFAULT true,
  ALTER COLUMN approval_loan_writeoff            SET DEFAULT true,
  ALTER COLUMN approval_loan_reschedule          SET DEFAULT true,
  ALTER COLUMN approval_loan_moratorium          SET DEFAULT true,
  ALTER COLUMN approval_loan_settlement_discount SET DEFAULT true;

-- ─────────── D. Existing-tenant backfill + per-field audit ───────────
-- Iterate every existing tenant_operations row and per-field:
--   • if currently false → set true + audit the flip
--   • leave already-true fields alone (no audit row)
-- This produces one audit row per (tenant × off-toggle), which is
-- the granularity the Recent-changes panel wants. RAISE NOTICE at
-- the end with the totals so the migration output makes the impact
-- visible.
DO $$
DECLARE
  r            record;
  flipped_cnt  int := 0;
  tenant_cnt   int := 0;
  fields       text[] := ARRAY[
    'approval_deposit', 'approval_withdrawal', 'approval_deposit_transfer',
    'approval_share_purchase', 'approval_share_transfer',
    'approval_share_bonus', 'approval_share_lien',
    'approval_loan_disbursement', 'approval_loan_repayment',
    'approval_loan_settle', 'approval_loan_reverse',
    'approval_loan_writeoff', 'approval_loan_reschedule',
    'approval_loan_moratorium', 'approval_loan_settlement_discount'
  ];
  f            text;
  current_val  boolean;
BEGIN
  FOR r IN SELECT tenant_id FROM tenant_operations
  LOOP
    tenant_cnt := tenant_cnt + 1;
    FOREACH f IN ARRAY fields
    LOOP
      EXECUTE format('SELECT %I FROM tenant_operations WHERE tenant_id = $1', f)
        INTO current_val
        USING r.tenant_id;
      IF current_val IS DISTINCT FROM true THEN
        EXECUTE format('UPDATE tenant_operations SET %I = true WHERE tenant_id = $1', f)
          USING r.tenant_id;
        INSERT INTO tenant_approval_changes (
          tenant_id, changed_by_user_id, field, old_value, new_value, reason
        ) VALUES (
          r.tenant_id, NULL, f, COALESCE(current_val::text, 'null'), 'true',
          'safe-by-default migration 0030 — opt out per-kind via Settings → Approvals'
        );
        flipped_cnt := flipped_cnt + 1;
      END IF;
    END LOOP;
  END LOOP;
  RAISE NOTICE 'approvals safe-by-default backfill: % tenant rows scanned, % field flips audited',
    tenant_cnt, flipped_cnt;
END $$;
