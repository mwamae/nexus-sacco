-- Reverse the backfill: clear the bridge cols + drop the
-- counterparty rows. The kind-distinguished WHERE clauses make this
-- safe to run after a partial Phase B rollout (only counterparties
-- that came from the backfill are deleted; new rows created via the
-- Phase B API path stay).
UPDATE org_members SET counterparty_id = NULL;
UPDATE members     SET counterparty_id = NULL;
DELETE FROM counterparties;
-- Reset the sequence so the next backfill mints from CP-YYYY-00001.
DELETE FROM share_number_seq WHERE kind = 'counterparty';
