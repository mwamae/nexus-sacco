-- share_transactions.journal_entry_id — handle for the GL entry tied
-- to this share movement.
--
-- Nullable, no FK. Cross-service FKs to journal_entries (accounting-
-- owned) aren't used in this codebase; same precedent as posting_outbox,
-- interest_runs, dividend_runs.
--
-- Semantic:
--   • IS NOT NULL — a GL entry was posted (purchase, adjustment, bonus)
--   • IS NULL     — by-design no GL move (transfer between members:
--                   equity-class-internal, Member Share Capital
--                   unchanged), or a pre-migration historical row.
--
-- Reconciliation primitive for the operations team — replaces the
-- spec's proposed share_transfers_audit / share_liens_audit tables,
-- which would duplicate the natural-key data already in
-- share_transactions + share_liens. Query:
--
--   SELECT * FROM share_transactions
--    WHERE counterparty_id = $member
--      AND journal_entry_id IS NULL          -- equity-internal moves
--      AND txn_type IN ('transfer_out', 'transfer_in');
--
-- Populated inside the same WithTenantTx that posts the GL entry
-- (services/savings/internal/handler/share.go::Purchase, Adjust,
-- BonusIssue). Transfer leaves it NULL by design. Lien place/release
-- don't write share_transactions at all.

ALTER TABLE share_transactions
  ADD COLUMN IF NOT EXISTS journal_entry_id uuid;

-- Index supports the "reconciliation gap" query: find moves that
-- should have a JE but don't. Partial on IS NULL keeps the index tiny
-- (post-migration the only NULL rows are transfers + historical
-- pre-this-migration rows, all of which are expected NULL).
CREATE INDEX IF NOT EXISTS share_transactions_no_je_idx
  ON share_transactions (tenant_id, txn_type, posted_at)
  WHERE journal_entry_id IS NULL;
