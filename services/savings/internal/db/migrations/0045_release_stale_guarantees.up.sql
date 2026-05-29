-- Phase 5 follow-up — release guarantor capacity that was left
-- committed after applications were declined or loans settled.
--
-- The guarantor-capacity calculation treats any loan_guarantees row
-- in status ∈ ('pending_consent','accepted','called_upon') as still
-- eating the guarantor's wallet. Two cases left rows stuck in those
-- states forever:
--
--   1. Application declined / cancelled / expired / offer_declined.
--      The application never became a loan; the guarantees should
--      have been released the moment the application failed but no
--      code path did that.
--
--   2. Loan settled / closed. The borrower paid the loan off in
--      full; the guarantor's obligation is satisfied.
--
-- This is a one-shot backfill. The Go code changes added in the same
-- PR (UpdateStatusTx + PostRepaymentTx hooks) prevent new rows from
-- getting stuck going forward.
--
-- Write-off (loan.status='written_off') is INTENTIONALLY EXCLUDED:
-- the SACCO may still call upon the guarantor to recover the loss,
-- so the obligation must persist. If a tenant has a policy to
-- release guarantees on write-off, run that update manually.

-- `notes` is qualified `g.notes` in the RHS because both
-- loan_guarantees and loan_applications (and loans) have a notes
-- column. Unqualified usage trips "column reference ambiguous"
-- (SQLSTATE 42702). The SET clause LHS stays unqualified — Postgres
-- requires that — only the RHS expression needs the alias.
UPDATE loan_guarantees g
   SET status      = 'released',
       released_at = COALESCE(g.released_at, now()),
       notes       = COALESCE(g.notes, '') ||
                     CASE WHEN COALESCE(g.notes,'') = '' THEN '' ELSE E'\n' END ||
                     '[backfill 0045] released — application is ' || a.status::text
  FROM loan_applications a
 WHERE g.application_id = a.id
   AND g.status IN ('pending_consent','accepted','called_upon')
   AND a.status IN ('declined','cancelled','expired','offer_declined');

UPDATE loan_guarantees g
   SET status      = 'released',
       released_at = COALESCE(g.released_at, now()),
       notes       = COALESCE(g.notes, '') ||
                     CASE WHEN COALESCE(g.notes,'') = '' THEN '' ELSE E'\n' END ||
                     '[backfill 0045] released — loan is ' || l.status::text
  FROM loans l
 WHERE g.loan_id = l.id
   AND g.status IN ('pending_consent','accepted','called_upon')
   AND l.status IN ('settled','closed');
