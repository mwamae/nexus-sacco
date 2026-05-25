-- dividend_runs.journal_entry_id — handle for the batched appropriation
-- journal entry produced when the run is posted.
--
-- Nullable, no FK. Cross-service FKs to journal_entries (owned by the
-- accounting service) aren't used in this codebase — interest_runs
-- migration 0033 set the same precedent. The "real" entry is
-- recoverable by joining on (source_module='savings.dividend',
-- source_ref=run.id).
--
-- Populated inside the same WithTenantTx that flips the run to
-- 'posted' (services/savings/internal/handler/dividend.go::Post).
-- A null journal_entry_id on a posted-state run signals the GL
-- write was suppressed (dev / Posting client disabled).

ALTER TABLE dividend_runs
  ADD COLUMN IF NOT EXISTS journal_entry_id uuid;
