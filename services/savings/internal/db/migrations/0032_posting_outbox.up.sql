-- Transactional outbox for GL postings.
--
-- Pre-this-migration every cash-touching handler called the HTTP
-- accounting client AFTER its WithTenantTx returned. That broke
-- the "no transaction is financially complete without a GL entry"
-- invariant: a brief accounting-service blip lost the post silently
-- while the deposit row stayed committed.
--
-- The fix: handlers now insert a row in posting_outbox INSIDE the
-- same tx as the business write. Business row + outbox row commit
-- atomically. A background dispatcher
-- (services/savings/cmd/posting-dispatcher) reads pending outbox
-- rows, calls the accounting service, and stamps success / failure
-- on the row. Retries with exponential backoff; hard-fails at 12
-- attempts so an on-call alert catches anything truly broken.
--
-- The table is shared across services that share the DB — savings
-- writes here from its own handlers; member writes here from the
-- application-fee path; the dispatcher drains every row regardless
-- of origin (the payload carries source_module so the accounting
-- service's dedup picks up the right semantics).

CREATE TABLE IF NOT EXISTS posting_outbox (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- The PostInput body the dispatcher will replay verbatim. Carries
  -- source_module + source_ref so the accounting service's
  -- (source_module, source_ref) dedup catches retries safely.
  payload       jsonb NOT NULL,
  attempts      int  NOT NULL DEFAULT 0,
  last_error    text,
  enqueued_at   timestamptz NOT NULL DEFAULT now(),
  -- Stamped by the dispatcher once the accounting service confirms.
  -- Outbox rows with dispatched_at IS NOT NULL are "done" and the
  -- dispatcher's polling query ignores them. Kept for audit /
  -- replay history rather than deleted.
  dispatched_at timestamptz,
  posted_je_id  uuid
);

-- Hot-path index for the dispatcher's poller. Filters pending
-- (dispatched_at IS NULL) rows scoped per tenant and ordered for
-- FIFO drain.
CREATE INDEX IF NOT EXISTS posting_outbox_pending_idx
  ON posting_outbox (tenant_id, dispatched_at, enqueued_at)
  WHERE dispatched_at IS NULL;

-- Powers the stuck-viewer endpoint: rows that the dispatcher has
-- tried >= 3 times but hasn't landed.
CREATE INDEX IF NOT EXISTS posting_outbox_stuck_idx
  ON posting_outbox (tenant_id, attempts DESC, enqueued_at)
  WHERE dispatched_at IS NULL AND attempts >= 3;

ALTER TABLE posting_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE posting_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_posting_outbox ON posting_outbox
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON posting_outbox TO nexus_app;
-- No DELETE grant. Outbox rows are kept for the audit trail and
-- so replay can find them by id; ops who need to purge old rows
-- use a superuser-bound retention script, not the app role.
