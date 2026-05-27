-- Adds tenant_operations.writeoff_through_provision so the loan-
-- writeoff JE shape can be flipped per tenant.
--
-- false (default) — DIRECT write-off:
--   DR 5020 Loan Provision Expense
--   CR 1100 Loans Receivable
-- Simpler accounting, doesn't require a maintained provision
-- allowance account. Matches what most small SACCOs do.
--
-- true — THROUGH-PROVISION write-off (IFRS 9 ECL-style):
--   DR 2900 Provision for Loan Losses (the allowance balance)
--   CR 1100 Loans Receivable
-- Assumes the tenant has been accruing provisions to 2900 via the
-- ECL run + maintains a non-zero allowance. Auditors prefer this
-- where applicable.
--
-- Idempotent (uses IF NOT EXISTS).

BEGIN;

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS writeoff_through_provision boolean NOT NULL DEFAULT false;

COMMIT;
