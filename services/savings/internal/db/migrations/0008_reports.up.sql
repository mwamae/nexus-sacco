-- ═══════════════════════════════════════════════════════════════════
-- Reporting additions (Phase 6f).
--
-- Adds:
--   • tenant_operations.provisioning_*_pct  — CBK/SASRA-aligned provisioning
--     percentages per classification band. Defaults: 1% watch, 25% substandard,
--     50% doubtful, 100% loss.
--   • loan_writeoffs                        — audit row per write-off action.
--     Captures the reason, prior balances, and any subsequent recoveries.
--   • loan_recoveries                       — payments received against a
--     written-off loan (post-writeoff recovery).
-- ═══════════════════════════════════════════════════════════════════

ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS provisioning_watch_pct       numeric(5,2) NOT NULL DEFAULT 1.00,
  ADD COLUMN IF NOT EXISTS provisioning_substandard_pct numeric(5,2) NOT NULL DEFAULT 25.00,
  ADD COLUMN IF NOT EXISTS provisioning_doubtful_pct    numeric(5,2) NOT NULL DEFAULT 50.00,
  ADD COLUMN IF NOT EXISTS provisioning_loss_pct        numeric(5,2) NOT NULL DEFAULT 100.00;

CREATE TABLE IF NOT EXISTS loan_writeoffs (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  loan_id              uuid NOT NULL UNIQUE REFERENCES loans(id) ON DELETE RESTRICT,
  member_id            uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  -- Snapshot of balances at write-off time.
  principal_written_off numeric(18,2) NOT NULL,
  interest_written_off  numeric(18,2) NOT NULL,
  fees_written_off      numeric(18,2) NOT NULL,
  penalty_written_off   numeric(18,2) NOT NULL,
  total_written_off     numeric(18,2) NOT NULL,
  reason               text NOT NULL,
  workflow_instance_id uuid,
  authorized_at        timestamptz NOT NULL DEFAULT now(),
  authorized_by        uuid NOT NULL,
  writeoff_txn_id      uuid REFERENCES loan_transactions(id) ON DELETE SET NULL,
  created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS loan_writeoffs_tenant_idx ON loan_writeoffs (tenant_id, authorized_at DESC);

CREATE TABLE IF NOT EXISTS loan_recoveries (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  writeoff_id     uuid NOT NULL REFERENCES loan_writeoffs(id) ON DELETE CASCADE,
  loan_id         uuid NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
  amount          numeric(18,2) NOT NULL CHECK (amount > 0),
  channel         text,
  channel_ref     text,
  narration       text,
  recovered_at    timestamptz NOT NULL DEFAULT now(),
  recovered_by    uuid NOT NULL
);
CREATE INDEX IF NOT EXISTS loan_recoveries_writeoff_idx ON loan_recoveries (writeoff_id, recovered_at DESC);

-- RLS + grants.
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['loan_writeoffs', 'loan_recoveries'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON loan_writeoffs, loan_recoveries TO nexus_app;
