-- ═══════════════════════════════════════════════════════════════════
-- Maker-checker for cash-handling transactions (Phase 7b).
--
-- When a per-kind toggle on tenant_operations is enabled, the original
-- action handler queues a pending_approvals row instead of posting to
-- the ledger directly. A second user reviews, then approves (executes)
-- or declines.
--
-- pending_approvals.payload is the original request body (as JSON) so
-- the executor can replay deterministically at approval time.
-- ═══════════════════════════════════════════════════════════════════

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS approval_deposit          boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_withdrawal       boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_deposit_transfer boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS approval_allow_self       boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN tenant_operations.approval_allow_self IS
  'Whether the same user can be both maker and checker. Defaults to false (proper segregation of duties).';

CREATE TABLE IF NOT EXISTS pending_approvals (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind               text NOT NULL,
  status             text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','approved','declined','cancelled','execution_error')),
  title              text NOT NULL,

  subject_member_id  uuid,
  subject_account_id uuid,
  subject_loan_id    uuid,
  amount             numeric(18,2),

  payload            jsonb NOT NULL,

  maker_user_id      uuid NOT NULL,
  maker_at           timestamptz NOT NULL DEFAULT now(),
  maker_note         text,

  checker_user_id    uuid,
  checker_at         timestamptz,
  checker_note       text,

  result_txn_id      uuid,
  result_error       text,

  created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS pending_approvals_tenant_status_idx
  ON pending_approvals (tenant_id, status, kind, created_at DESC);

CREATE INDEX IF NOT EXISTS pending_approvals_subject_idx
  ON pending_approvals (tenant_id, subject_member_id, created_at DESC);

ALTER TABLE pending_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE pending_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_pending_approvals ON pending_approvals
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON pending_approvals TO nexus_app;
