-- loan_product_fees.gl_credit_code — per-fee credit account for the
-- batched disbursement journal entry.
--
-- Bug this closes: the loan-disbursement GL post used to credit the
-- channel cash account for the full principal, ignoring upfront fees
-- entirely. Results:
--   • 4010 Loan Processing Fee Income and 4020 Loan Insurance/LPF
--     Income stayed at zero on the Income Statement.
--   • Cash-side accounts (1030 M-Pesa Float, 1020 Bank Current, …)
--     were over-credited by the fee total — trial-balance drift.
--
-- The handler now reads gl_credit_code per fee and credits each one
-- explicitly; the cash leg drops to net (principal − Σ fees).
--
-- Mirrors the fee_catalog.gl_credit_code precedent from PR fee-coa
-- (migration 0031). Tenants edit the value via the loan-product UI;
-- this migration backfills sensible defaults from the fee name so
-- existing rows aren't blocked when the new code path goes live.

ALTER TABLE loan_product_fees
  ADD COLUMN IF NOT EXISTS gl_credit_code text;

-- Name-pattern backfill. Order matters — the insurance and CRB
-- matches are checked before the catch-all 4010 fallback. Idempotent:
-- only updates rows where the column is currently NULL.

UPDATE loan_product_fees
   SET gl_credit_code = '4020'
 WHERE gl_credit_code IS NULL
   AND (name ILIKE '%insurance%' OR name ILIKE '%LPF%');

UPDATE loan_product_fees
   SET gl_credit_code = '4190'
 WHERE gl_credit_code IS NULL
   AND (name ILIKE '%appraisal%' OR name ILIKE '%CRB%'
        OR name ILIKE '%ledger%' OR name ILIKE '%SMS%');

UPDATE loan_product_fees
   SET gl_credit_code = '4010'
 WHERE gl_credit_code IS NULL;

ALTER TABLE loan_product_fees
  ALTER COLUMN gl_credit_code SET NOT NULL,
  ALTER COLUMN gl_credit_code SET DEFAULT '4010';
