-- Phase 3 of the M-PESA integration: engine + distributor (defer-apply variant).
--
-- This migration is bookkeeping-only. No GL-touching schema lives
-- here — the actual deposit / loan-repayment / fee writes are
-- deferred to phase 3.5 when the shared finance package gets
-- extracted from services/savings (see the Step-0 brief in the
-- phase-3 PR for why).
--
-- What's added:
--   • mpesa_inbound_events: attempts, error_text, posted_at,
--     distribution_run_id, locked_at, locked_by — together they
--     give the distributor enough state to do retries + the
--     concurrency-safe SELECT … FOR UPDATE SKIP LOCKED pattern.
--   • receipt_lines.external_validation_ref — column reserved for
--     phase 3.5. mpesa stamps it with the MpesaReceiptNumber when
--     it writes a receipt-line row (phase 3.5 turns on the writes).
--     The collection-desk approval router will skip approval when
--     this field is set, since the rail itself already validated
--     the transaction.
--   • mpesa_distribution_runs.cash_account_code + clearing_account_code
--     — the GL accounts the engine debits / credits when posting
--     the "money parked in clearing" entry for the inbound event.
--
-- Waterfall jsonb shape (mpesa_distribution_policies.waterfall):
--   {
--     "legs": [
--       {"target": "fees_due"},
--       {"target": "loan_penalty_due"},
--       {"target": "loan_interest_due"},
--       {"target": "loan_principal_due"},
--       {"target": "bosa_top_up"},
--       {"target": "fosa_top_up"}
--     ]
--   }
-- The engine walks legs in order, asks the appropriate read-only
-- balance store for the outstanding amount, builds a Split with
-- min(remaining, balance), reduces remaining, moves on. The LAST
-- leg gets any leftover. Targets the engine doesn't recognise are
-- skipped with a warning so an unfamiliar policy doesn't drop the
-- whole event.
--
-- mpesa_distribution_runs.splits jsonb shape (per row produced by
-- engine.Run):
--   [
--     {"leg": "fees_due",          "amount": "200.00", "target_ref": "FEE-MEMBERSHIP"},
--     {"leg": "loan_interest_due", "amount": "500.00", "target_ref": "L-2025-00042"},
--     {"leg": "loan_principal_due","amount": "2000.00","target_ref": "L-2025-00042"},
--     {"leg": "bosa_top_up",       "amount": "1300.00","target_ref": "DA-1234567"}
--   ]
-- target_ref is the human-readable identifier of whatever the split
-- lands against — fee code, loan_no, deposit account_no, etc. Phase
-- 3.5's applier turns each split into the matching savings-side write.

-- ─────────── mpesa_inbound_events: distributor state ───────────
ALTER TABLE mpesa_inbound_events
  ADD COLUMN IF NOT EXISTS attempts             integer     NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS error_text           text,
  ADD COLUMN IF NOT EXISTS posted_at            timestamptz,
  ADD COLUMN IF NOT EXISTS distribution_run_id  uuid,
  -- locked_at / locked_by give us a "this worker is processing it"
  -- breadcrumb in addition to FOR UPDATE's row lock. Useful when
  -- a worker crashes mid-tx and we want to see what was in flight.
  ADD COLUMN IF NOT EXISTS locked_at            timestamptz,
  ADD COLUMN IF NOT EXISTS locked_by            uuid;

-- Index the distributor uses to pick the next-due row.
CREATE INDEX IF NOT EXISTS mpesa_inbound_events_distributor_idx
  ON mpesa_inbound_events (tenant_id, status, attempts, received_at)
  WHERE status = 'received';

-- ─────────── mpesa_distribution_runs: GL bookkeeping fields ───────────
ALTER TABLE mpesa_distribution_runs
  ADD COLUMN IF NOT EXISTS cash_account_code     text,
  ADD COLUMN IF NOT EXISTS clearing_account_code text,
  ADD COLUMN IF NOT EXISTS resolved_member_id    uuid,
  ADD COLUMN IF NOT EXISTS resolved_via          mpesa_resolved_via,
  ADD COLUMN IF NOT EXISTS amount                numeric(18,2);

-- ─────────── receipt_lines: reserved column for phase 3.5 ───────────
-- Cross-service additive column. Owned by savings; mpesa just
-- writes to it. Indexing on it costs little — the collection-desk
-- router needs the column on every row it processes anyway.
ALTER TABLE receipt_lines
  ADD COLUMN IF NOT EXISTS external_validation_ref text;

CREATE INDEX IF NOT EXISTS receipt_lines_external_validation_ref_idx
  ON receipt_lines (external_validation_ref) WHERE external_validation_ref IS NOT NULL;

COMMENT ON COLUMN receipt_lines.external_validation_ref IS
  'External-rail validation ID (e.g. Safaricom MpesaReceiptNumber). When non-NULL the collection-desk approval router skips the per-kind approval gate — the external rail already validated the transaction. Owned by services/mpesa; consumed by services/savings.';
