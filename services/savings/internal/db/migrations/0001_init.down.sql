DROP TABLE IF EXISTS share_number_seq;
DROP TABLE IF EXISTS share_certificates;
DROP TABLE IF EXISTS share_liens;
DROP TABLE IF EXISTS share_transactions;
DROP TABLE IF EXISTS share_accounts;
DROP TYPE IF EXISTS share_lien_status;
DROP TYPE IF EXISTS share_payment_channel;
DROP TYPE IF EXISTS share_txn_type;
DROP TYPE IF EXISTS share_account_status;

ALTER TABLE tenant_operations
  DROP COLUMN IF EXISTS dividend_wht_rate,
  DROP COLUMN IF EXISTS share_certificate_prefix,
  DROP COLUMN IF EXISTS max_shares_pct_of_capital,
  DROP COLUMN IF EXISTS min_shares_required,
  DROP COLUMN IF EXISTS share_par_value;
