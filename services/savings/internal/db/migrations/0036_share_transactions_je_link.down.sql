DROP INDEX IF EXISTS share_transactions_no_je_idx;
ALTER TABLE share_transactions DROP COLUMN IF EXISTS journal_entry_id;
