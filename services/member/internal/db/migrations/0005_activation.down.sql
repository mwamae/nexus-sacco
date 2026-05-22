ALTER TABLE membership_applications
  DROP COLUMN IF EXISTS fee_refund_journal_entry_id,
  DROP COLUMN IF EXISTS fee_journal_entry_id,
  DROP COLUMN IF EXISTS materialized_at,
  DROP COLUMN IF EXISTS materialized_member_id;
