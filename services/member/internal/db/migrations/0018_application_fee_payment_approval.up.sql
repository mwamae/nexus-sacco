-- Wave 2 of the approval-coverage rollout — route application
-- registration-fee payments through the maker-checker queue when
-- approval_application_fee is on.
--
-- The handler now branches on the toggle:
--   • OFF → existing inline path: insert + post JE immediately.
--   • ON  → insert with journal_entry_id NULL + stamp approval_id;
--          create a pending_approvals row of kind
--          'application_fee'. The savings-side approval dispatcher
--          posts the JE on approve and stamps journal_entry_id.
--
-- approval_id is intentionally a plain uuid (no FK). Pending
-- approvals live in the savings service's pending_approvals table
-- (shared DB but a different logical owner); the cross-schema FK
-- would couple migration ordering across services. The materialise
-- pre-check joins explicitly when it needs to.

ALTER TABLE application_fee_payments
  ADD COLUMN IF NOT EXISTS approval_id uuid;

-- Used by the materialise pre-check that 409s when any non-voided
-- payment is still pending approval.
CREATE INDEX IF NOT EXISTS application_fee_payments_approval_idx
  ON application_fee_payments (approval_id)
  WHERE approval_id IS NOT NULL AND voided_at IS NULL;
