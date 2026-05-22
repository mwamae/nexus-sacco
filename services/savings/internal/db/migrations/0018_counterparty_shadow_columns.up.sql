-- Phase C, Tier 2 — counterparty_id shadow columns on every savings
-- table that today FKs to members.id.
--
-- Additive: every column is nullable, backfilled from the existing
-- members.counterparty_id bridge (populated by member migration
-- 0008), and kept in sync going forward via a BEFORE INSERT trigger
-- that copies counterparty_id from the matching members row whenever
-- counterparty_id is omitted. Reads are NOT changed in this PR — every
-- query path still uses member_id. Dropping member_id is the next
-- (destructive) PR, after this groundwork has soaked.
--
-- Triggers are preferred over Go-side dual-write here because every
-- savings store has multiple insert paths; one trigger per table is
-- both fewer lines AND impossible to drift past.
--
-- Coverage: 13 tables across the savings service. Each gets:
--   1. ALTER TABLE ADD COLUMN counterparty_id uuid
--      REFERENCES counterparties(id) ON DELETE RESTRICT
--   2. CREATE INDEX (partial, WHERE NOT NULL)
--   3. Backfill UPDATE … FROM members m WHERE table.member_id = m.id
--   4. BEFORE INSERT trigger that sets NEW.counterparty_id from the
--      matching members.counterparty_id when NEW.counterparty_id IS NULL.
--
-- The single deposit_accounts.guardian_member_id column is left as a
-- follow-up — guardians are rare in the seed data and the column is
-- semantically distinct enough that a one-shot rename would help.

-- Helper: a single trigger function shared by every table. Reads
-- NEW.member_id (the column name is consistent across all 12 of the
-- 13 tables; loan_guarantees passes its guarantor_member_id via a
-- dedicated trigger function below).
CREATE OR REPLACE FUNCTION populate_counterparty_id_from_member()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.counterparty_id IS NULL AND NEW.member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.counterparty_id
      FROM members WHERE id = NEW.member_id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Per-table apply helper as a DO block — keeps the file readable.
DO $$
DECLARE
  t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'share_accounts', 'share_certificates', 'share_transactions',
    'deposit_accounts', 'deposit_transactions',
    'interest_run_lines', 'tax_payable_ledger', 'dividend_run_lines',
    'loan_applications', 'loans', 'loan_transactions',
    'loan_collection_cases'
  ]) LOOP
    EXECUTE format('ALTER TABLE %I ADD COLUMN IF NOT EXISTS counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT', t);
    EXECUTE format('CREATE INDEX IF NOT EXISTS %I_counterparty_idx ON %I (counterparty_id) WHERE counterparty_id IS NOT NULL', t, t);
    -- Backfill from the legacy bridge.
    EXECUTE format(
      'UPDATE %I tbl SET counterparty_id = m.counterparty_id FROM members m WHERE tbl.member_id = m.id AND tbl.counterparty_id IS NULL',
      t);
    -- Trigger.
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_populate_counterparty ON %I', t, t);
    EXECUTE format(
      'CREATE TRIGGER trg_%I_populate_counterparty BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION populate_counterparty_id_from_member()',
      t, t);
  END LOOP;
END $$;

-- loan_guarantees uses guarantor_member_id (not member_id), so it
-- gets its own trigger function + dedicated wiring.
ALTER TABLE loan_guarantees
  ADD COLUMN IF NOT EXISTS guarantor_counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS loan_guarantees_guarantor_cp_idx
  ON loan_guarantees (guarantor_counterparty_id) WHERE guarantor_counterparty_id IS NOT NULL;

UPDATE loan_guarantees g
   SET guarantor_counterparty_id = m.counterparty_id
  FROM members m
 WHERE g.guarantor_member_id = m.id
   AND g.guarantor_counterparty_id IS NULL;

CREATE OR REPLACE FUNCTION populate_guarantor_counterparty_id()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.guarantor_counterparty_id IS NULL AND NEW.guarantor_member_id IS NOT NULL THEN
    SELECT counterparty_id INTO NEW.guarantor_counterparty_id
      FROM members WHERE id = NEW.guarantor_member_id;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_loan_guarantees_populate_guarantor_cp ON loan_guarantees;
CREATE TRIGGER trg_loan_guarantees_populate_guarantor_cp
  BEFORE INSERT ON loan_guarantees
  FOR EACH ROW EXECUTE FUNCTION populate_guarantor_counterparty_id();
