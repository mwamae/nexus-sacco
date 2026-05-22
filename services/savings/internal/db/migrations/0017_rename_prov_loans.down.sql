-- No down. Reversing the rename is impossible — the original PROV-*
-- numbers used <unix-ts> suffixes that the renumber doesn't preserve.
-- If a rollback is genuinely needed, handle by a one-off SQL session
-- that knows which loans to renumber back from a snapshot.
SELECT 1;
