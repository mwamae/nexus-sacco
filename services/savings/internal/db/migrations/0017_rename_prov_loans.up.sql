-- One-off renumber of the provisioning seed scaffold loans.
--
-- A short-lived test scaffold (no longer in the repo) inserted three
-- loans + matching loan_applications with synthetic numbers shaped
-- "L-PROV-{W,S,L}-<unix-ts>" + "PROV-{W,S,L}-<unix-ts>". The shape
-- doesn't match the documented numbering scheme
-- (docs/lending-number-formats.md), shows up to admins in the lending
-- UI, and makes the loan_no column look like there's a parallel
-- numbering system that doesn't actually exist.
--
-- This migration renumbers those rows in place using fresh numbers
-- from the existing per-tenant share_number_seq counters. It is
-- intentionally NOT a backfill of all historical loans — only the
-- PROV-prefixed scaffold rows are touched. application_no and loan_no
-- are not foreign keys (only the UUIDs are), so renaming is safe; no
-- downstream rows need updating.
--
-- Idempotent: on a DB without any PROV- rows (i.e. fresh production
-- or any CI/dev that didn't run the old scaffold) the DO block has
-- nothing to iterate over and the migration is a no-op.

DO $$
DECLARE
  r RECORD;
  new_app_no  text;
  new_loan_no text;
  app_seq     int;
  loan_seq    int;
  yr          int := EXTRACT(YEAR FROM now() AT TIME ZONE 'UTC')::int;
BEGIN
  FOR r IN
    SELECT l.id          AS loan_id,
           l.tenant_id   AS tenant_id,
           l.loan_no     AS old_loan_no,
           a.id          AS app_id,
           a.application_no AS old_app_no
      FROM loans l
      JOIN loan_applications a ON a.id = l.application_id
     WHERE l.loan_no        LIKE 'L-PROV-%'
        OR a.application_no LIKE 'PROV-%'
     ORDER BY l.created_at
  LOOP
    -- Mint a fresh loan_application number (LA-YYYY-NNNNN).
    INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
    VALUES (r.tenant_id, 'loan_application', yr, 1)
    ON CONFLICT (tenant_id, kind, year)
    DO UPDATE SET last_value = share_number_seq.last_value + 1
    RETURNING last_value INTO app_seq;
    new_app_no := format('LA-%s-%s', yr, lpad(app_seq::text, 5, '0'));

    -- Mint a fresh loan number (L-YYYY-NNNNN).
    INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
    VALUES (r.tenant_id, 'loan', yr, 1)
    ON CONFLICT (tenant_id, kind, year)
    DO UPDATE SET last_value = share_number_seq.last_value + 1
    RETURNING last_value INTO loan_seq;
    new_loan_no := format('L-%s-%s', yr, lpad(loan_seq::text, 5, '0'));

    UPDATE loan_applications SET application_no = new_app_no  WHERE id = r.app_id;
    UPDATE loans             SET loan_no        = new_loan_no WHERE id = r.loan_id;

    RAISE NOTICE 'renumber PROV row: tenant=% old_loan=% new_loan=% old_app=% new_app=%',
      r.tenant_id, r.old_loan_no, new_loan_no, r.old_app_no, new_app_no;
  END LOOP;
END $$;
