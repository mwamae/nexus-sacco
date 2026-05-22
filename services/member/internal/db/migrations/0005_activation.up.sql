-- ═══════════════════════════════════════════════════════════════════
-- Activation pipeline linkage (Phase 12/D).
--
-- When the approver flips an application to approved_active, the
-- system auto-materialises the member row + share account + savings
-- account + posts the registration-fee journal entry. These columns
-- link the application back to the resulting artefacts so the UI can
-- show "approved → member M-2026-00099 created" and the audit trail
-- holds the full chain.
-- ═══════════════════════════════════════════════════════════════════

ALTER TABLE membership_applications
  ADD COLUMN IF NOT EXISTS materialized_member_id  uuid REFERENCES members(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS materialized_at         timestamptz,
  ADD COLUMN IF NOT EXISTS fee_journal_entry_id    uuid,  -- accounting service journal_entries.id (no FK — different schema)
  ADD COLUMN IF NOT EXISTS fee_refund_journal_entry_id uuid;

CREATE INDEX IF NOT EXISTS applications_materialized_idx
  ON membership_applications (materialized_member_id) WHERE materialized_member_id IS NOT NULL;
