-- ═══════════════════════════════════════════════════════════════════
-- Cash & Float Management (Phase 11/6).
--
-- Operational tracking of physical cash across vault + tellers:
--
--   tills            — physical till identifiers (cash drawers)
--   till_sessions    — one per teller's shift assignment to a till;
--                      tracks opening float, expected balance, actual
--                      count + variance
--   cash_transfers   — vault↔till and till↔till movements, each
--                      tied to an auto-posted journal entry
--
-- GL posting rules:
--   vault → till:    DR 1010 Cash at Till   / CR 1000 Cash on Hand
--   till → vault:    DR 1000 Cash on Hand   / CR 1010 Cash at Till
--   till → till:     no GL movement (same account; operational only)
--   variance short:  DR 2250 Variance       / CR 1010
--   variance over:   DR 1010                / CR 2250
--
-- The GL doesn't have per-till sub-accounts — 1010 aggregates every
-- till. Per-till expected balances are tracked in till_sessions +
-- cash_transfers; the reconciliation between operational and GL views
-- is checked by the cash-position report.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS tills (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code                 text NOT NULL,
  name                 text NOT NULL,
  branch               text,
  gl_account_code      text NOT NULL DEFAULT '1010',
  vault_account_code   text NOT NULL DEFAULT '1000',
  variance_account_code text NOT NULL DEFAULT '2250',
  max_float            numeric(18,2),
  is_active            boolean NOT NULL DEFAULT true,
  notes                text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);

CREATE INDEX IF NOT EXISTS tills_tenant_idx ON tills (tenant_id, is_active);

ALTER TABLE tills ENABLE ROW LEVEL SECURITY;
ALTER TABLE tills FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_tills ON tills
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON tills TO nexus_app;


CREATE TABLE IF NOT EXISTS till_sessions (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  till_id         uuid NOT NULL REFERENCES tills(id) ON DELETE CASCADE,
  teller_user_id  uuid NOT NULL,
  status          text NOT NULL DEFAULT 'open'
                   CHECK (status IN ('open', 'closed')),
  opening_float   numeric(18,2) NOT NULL,
  expected_close  numeric(18,2) NOT NULL DEFAULT 0,
  actual_close    numeric(18,2),
  variance        numeric(18,2) NOT NULL DEFAULT 0,
  variance_journal_entry_id uuid REFERENCES journal_entries(id) ON DELETE SET NULL,
  opened_at       timestamptz NOT NULL DEFAULT now(),
  opened_by       uuid NOT NULL,
  closed_at       timestamptz,
  closed_by       uuid,
  notes           text
);

CREATE INDEX IF NOT EXISTS till_sessions_till_idx ON till_sessions (till_id, opened_at DESC);
CREATE INDEX IF NOT EXISTS till_sessions_teller_idx ON till_sessions (teller_user_id, opened_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS till_sessions_one_open_per_till
  ON till_sessions (till_id) WHERE status = 'open';

ALTER TABLE till_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE till_sessions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_till_sessions ON till_sessions
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON till_sessions TO nexus_app;


CREATE TABLE IF NOT EXISTS cash_transfers (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  transfer_type       text NOT NULL
                       CHECK (transfer_type IN ('vault_to_till','till_to_vault','till_to_till','opening_float','closing_return','variance_adjustment')),
  from_till_id        uuid REFERENCES tills(id) ON DELETE SET NULL,
  to_till_id          uuid REFERENCES tills(id) ON DELETE SET NULL,
  session_id          uuid REFERENCES till_sessions(id) ON DELETE SET NULL,
  amount              numeric(18,2) NOT NULL CHECK (amount > 0),
  reference           text,
  narration           text,
  journal_entry_id    uuid REFERENCES journal_entries(id) ON DELETE SET NULL,
  transferred_at      timestamptz NOT NULL DEFAULT now(),
  transferred_by      uuid NOT NULL
);

CREATE INDEX IF NOT EXISTS cash_transfers_tenant_idx ON cash_transfers (tenant_id, transferred_at DESC);
CREATE INDEX IF NOT EXISTS cash_transfers_session_idx ON cash_transfers (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS cash_transfers_till_from_idx ON cash_transfers (from_till_id) WHERE from_till_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS cash_transfers_till_to_idx ON cash_transfers (to_till_id) WHERE to_till_id IS NOT NULL;

ALTER TABLE cash_transfers ENABLE ROW LEVEL SECURITY;
ALTER TABLE cash_transfers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_cash_transfers ON cash_transfers
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON cash_transfers TO nexus_app;
