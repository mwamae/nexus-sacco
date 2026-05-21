-- Drop everything in reverse dependency order. Enum drops last.
DROP TABLE IF EXISTS posting_rules;
DROP TABLE IF EXISTS journal_lines;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS accounting_periods;
DROP TABLE IF EXISTS chart_of_accounts;

DROP TYPE IF EXISTS accounting_period_status;
DROP TYPE IF EXISTS journal_entry_type;
DROP TYPE IF EXISTS journal_entry_status;
DROP TYPE IF EXISTS account_normal_balance;
DROP TYPE IF EXISTS account_class;
