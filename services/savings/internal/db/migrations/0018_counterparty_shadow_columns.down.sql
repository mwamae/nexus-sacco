-- Drop triggers + columns. Idempotent.
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'share_accounts', 'share_certificates', 'share_transactions',
    'deposit_accounts', 'deposit_transactions',
    'interest_run_lines', 'tax_payable_ledger', 'dividend_run_lines',
    'loan_applications', 'loans', 'loan_transactions',
    'loan_collection_cases'
  ]) LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_populate_counterparty ON %I', t, t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN IF EXISTS counterparty_id', t);
  END LOOP;
END $$;

DROP TRIGGER IF EXISTS trg_loan_guarantees_populate_guarantor_cp ON loan_guarantees;
ALTER TABLE loan_guarantees DROP COLUMN IF EXISTS guarantor_counterparty_id;

DROP FUNCTION IF EXISTS populate_counterparty_id_from_member();
DROP FUNCTION IF EXISTS populate_guarantor_counterparty_id();
