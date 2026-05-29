-- No rollback. Recreating the trigger would re-introduce the
-- "NEW.guarantor_member_id" reference on a row that no longer has
-- that column. The function would error on the next INSERT — the
-- exact bug this migration fixes.
--
-- If you genuinely need to roll back, restore the column first:
--   ALTER TABLE loan_guarantees ADD COLUMN guarantor_member_id uuid;
-- then recreate the trigger function as it was in the Phase D draft.

SELECT 1;
