-- Augment wf_instances so the new callback-dispatcher worker can
-- treat the row itself as an outbox entry. The legacy fireCallback
-- was a fire-and-forget goroutine in the Action HTTP handler — when
-- savings was momentarily unavailable the POST was lost, the row
-- showed callback_status='failed:transport:…', and an operator had
-- to manually re-fire by replaying the approval. The dispatcher
-- replaces that with the same retry-and-DLQ pattern the
-- posting-dispatcher uses.
--
-- New columns:
--
--   callback_attempts        — how many times the dispatcher has tried
--                              this row. Caps at 12 (matches the
--                              posting-outbox cap).
--   callback_next_attempt_at — earliest time the dispatcher should
--                              retry. Set by the dispatcher after a
--                              failed attempt; ignored on first attempt
--                              (NULL means "try now"). Exponential
--                              backoff with jitter is computed by the
--                              worker, not the DB.
--   callback_last_error      — last failure message, if any. Useful
--                              for the Approvals Inbox UI to surface
--                              "callback failed: <reason>" inline.
--
-- callback_status semantics tighten:
--   NULL or 'pending'                → ready for next attempt
--   'in_flight'                      → the dispatcher claimed this row
--                                      via SELECT FOR UPDATE SKIP LOCKED
--                                      (set + held until POST returns)
--   'delivered'                      → terminal, success
--   'failed:<msg>'                   → terminal, failed after 12 attempts
--   'failed:executor:<msg>'          → terminal, savings returned 2xx but
--                                      its executor rejected the payload;
--                                      see callback_last_error for detail
--
-- The DLQ surface is "every row where callback_status LIKE 'failed:%'".
-- Operators triage from there.

ALTER TABLE wf_instances
  ADD COLUMN IF NOT EXISTS callback_attempts        integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS callback_next_attempt_at timestamptz,
  ADD COLUMN IF NOT EXISTS callback_last_error      text;

-- Index supports the dispatcher's claim query:
--   SELECT id FROM wf_instances
--    WHERE callback_url IS NOT NULL
--      AND callback_status = 'pending'
--      AND (callback_next_attempt_at IS NULL OR callback_next_attempt_at <= now())
--    ORDER BY id
--    FOR UPDATE SKIP LOCKED LIMIT $batch_size;
--
-- Partial on the actionable subset so it stays tiny — once a row
-- reaches a terminal callback_status it drops out of the index.
CREATE INDEX IF NOT EXISTS wf_instances_callback_ready_idx
  ON wf_instances (callback_next_attempt_at NULLS FIRST, id)
  WHERE callback_url IS NOT NULL
    AND callback_status = 'pending';

COMMENT ON COLUMN wf_instances.callback_attempts IS
  'Dispatcher attempt counter. Caps at 12; row becomes DLQ after that.';
COMMENT ON COLUMN wf_instances.callback_next_attempt_at IS
  'Earliest next-attempt time. NULL = try now (first attempt or just-marked-pending).';
COMMENT ON COLUMN wf_instances.callback_last_error IS
  'Last failure reason from the dispatcher. Cleared on success.';
