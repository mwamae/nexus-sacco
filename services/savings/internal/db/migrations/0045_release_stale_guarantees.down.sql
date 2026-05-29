-- No rollback. Released guarantees should remain released — the
-- borrower's application failed (or their loan settled) and the
-- guarantor's obligation is genuinely over.
--
-- If you absolutely need to reverse, hand-write an UPDATE that
-- inspects the note tag '[backfill 0045]' and restores the prior
-- status from the application / loan state. Don't auto-undo a
-- backfill — it would re-erode capacity for guarantors whose
-- obligations have correctly ended.

SELECT 1;
