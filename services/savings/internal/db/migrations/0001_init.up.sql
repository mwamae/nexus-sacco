-- ═══════════════════════════════════════════════════════════════════
-- SAVINGS service initial schema — Shares sub-module.
--
-- Shares represent member EQUITY in the SACCO (capital contribution),
-- not deposits. They live on the equity side of the balance sheet.
-- Tracked here:
--   • share_accounts        — one per member, current balance + lien
--   • share_transactions    — append-only ledger with running balance
--   • share_liens           — pledges against loans; block transfer/redeem
--   • share_certificates    — versioned printable certificates
--   • per-tenant sequences  — for account / txn / certificate numbers
--
-- Lives in the same database as identity + member so we can FK to
-- tenants and members, and reuse the nexus_app application role.
-- ═══════════════════════════════════════════════════════════════════

-- ─────────── Tenant-level configuration ───────────
-- Per-tenant share policy lives on the existing tenant_operations table.
-- Owned by identity in migration 0007 — we extend it here.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS share_par_value             numeric(18,2) NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS min_shares_required         int           NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS max_shares_pct_of_capital   numeric(5,2)  NOT NULL DEFAULT 0,   -- 0 = uncapped
  ADD COLUMN IF NOT EXISTS share_certificate_prefix    text          NOT NULL DEFAULT 'SC',
  ADD COLUMN IF NOT EXISTS dividend_wht_rate           numeric(5,2)  NOT NULL DEFAULT 5;

COMMENT ON COLUMN tenant_operations.share_par_value IS
  'Fixed price per share. Shares are sold at par; SACCOs do not float share value.';
COMMENT ON COLUMN tenant_operations.min_shares_required IS
  'Minimum shares a member must hold to be in good standing.';
COMMENT ON COLUMN tenant_operations.max_shares_pct_of_capital IS
  'Cap as % of total SACCO share capital; 0 means uncapped.';

-- ─────────── share_accounts ───────────
-- One per member. shares_held is denormalised running balance (the
-- ledger in share_transactions is the source of truth, but we cache
-- the balance for fast reads + atomic comparison-and-update).
CREATE TYPE share_account_status AS ENUM ('active', 'closed');

CREATE TABLE share_accounts (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  member_id           uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  account_no          text NOT NULL,                          -- e.g. SHA-2026-00001
  status              share_account_status NOT NULL DEFAULT 'active',
  shares_held         int NOT NULL DEFAULT 0 CHECK (shares_held >= 0),
  shares_pledged      int NOT NULL DEFAULT 0 CHECK (shares_pledged >= 0 AND shares_pledged <= shares_held),
  par_value_at_open   numeric(18,2) NOT NULL,                 -- snapshot of policy when account opened
  first_purchase_at   timestamptz,
  closed_at           timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, member_id),
  UNIQUE (tenant_id, account_no)
);
CREATE INDEX share_accounts_member_idx ON share_accounts (member_id);
CREATE INDEX share_accounts_tenant_status_idx ON share_accounts (tenant_id, status);
CREATE TRIGGER share_accounts_updated_at BEFORE UPDATE ON share_accounts
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ─────────── share_transactions ───────────
-- Append-only equity ledger. shares_delta is signed:
--   purchase, transfer_in, bonus_issue, adjustment_credit → positive
--   transfer_out, redemption, adjustment_debit            → negative
-- amount mirrors shares_delta * par_value_at_txn (signed); we store it
-- explicitly so cents from any historical par-value change are preserved.
CREATE TYPE share_txn_type AS ENUM (
  'purchase',
  'transfer_in',
  'transfer_out',
  'redemption',
  'adjustment',
  'bonus_issue'
);

CREATE TYPE share_payment_channel AS ENUM (
  'cash',
  'mpesa',
  'airtel_money',
  'bank_transfer',
  'payroll',
  'standing_order',
  'internal'   -- transfers / bonus / adjustments
);

CREATE TABLE share_transactions (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id               uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  member_id                uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  txn_no                   text NOT NULL,                          -- e.g. SHT-2026-00001
  txn_type                 share_txn_type NOT NULL,
  shares_delta             int NOT NULL CHECK (shares_delta <> 0),
  par_value_at_txn         numeric(18,2) NOT NULL,
  amount                   numeric(18,2) NOT NULL,
  payment_channel          share_payment_channel,
  payment_ref              text,
  narration                text,
  counterparty_account_id  uuid REFERENCES share_accounts(id) ON DELETE RESTRICT, -- for transfers
  counterparty_txn_id      uuid,                                    -- linked twin row for transfers
  balance_after_shares     int NOT NULL CHECK (balance_after_shares >= 0),
  balance_after_amount     numeric(18,2) NOT NULL,
  initiated_by             uuid NOT NULL,                           -- identity user
  authorized_by            uuid,                                    -- second auth (adjustments / redemption)
  authorization_reason     text,
  posted_at                timestamptz NOT NULL DEFAULT now(),
  created_at               timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, txn_no)
);
CREATE INDEX share_txn_account_idx ON share_transactions (account_id, posted_at DESC);
CREATE INDEX share_txn_member_idx  ON share_transactions (member_id, posted_at DESC);
CREATE INDEX share_txn_tenant_type_idx ON share_transactions (tenant_id, txn_type, posted_at DESC);

-- ─────────── share_liens ───────────
-- Active pledges against loans / collateral. While active, the pledged
-- share count is reflected on share_accounts.shares_pledged and may not
-- be transferred or redeemed.
CREATE TYPE share_lien_status AS ENUM ('active', 'released');

CREATE TABLE share_liens (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id      uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  shares_pledged  int  NOT NULL CHECK (shares_pledged > 0),
  reason          text NOT NULL,                       -- "Loan L-2026-00012" etc
  reference_kind  text,                                -- "loan" | "collateral" | "manual"
  reference_id    text,                                -- external id (loan no, etc)
  status          share_lien_status NOT NULL DEFAULT 'active',
  placed_by       uuid NOT NULL,
  placed_at       timestamptz NOT NULL DEFAULT now(),
  released_by     uuid,
  released_at     timestamptz,
  released_reason text
);
CREATE INDEX share_liens_account_active_idx ON share_liens (account_id) WHERE status = 'active';
CREATE INDEX share_liens_tenant_ref_idx ON share_liens (tenant_id, reference_kind, reference_id);

-- ─────────── share_certificates ───────────
-- Versioned. Every share-changing transaction may retire the previous
-- certificate and issue a new one reflecting the post-txn balance.
CREATE TABLE share_certificates (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  account_id          uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  member_id           uuid NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
  certificate_no      text NOT NULL,                          -- e.g. SC-2026-00001
  shares_covered      int NOT NULL CHECK (shares_covered >= 0),
  par_value_at_issue  numeric(18,2) NOT NULL,
  total_value         numeric(18,2) NOT NULL,
  issued_at           timestamptz NOT NULL DEFAULT now(),
  retired_at          timestamptz,
  supersedes_id       uuid REFERENCES share_certificates(id) ON DELETE SET NULL,
  issued_by           uuid NOT NULL,
  UNIQUE (tenant_id, certificate_no)
);
CREATE INDEX share_certificates_account_idx ON share_certificates (account_id, issued_at DESC);
CREATE INDEX share_certificates_current_idx ON share_certificates (account_id) WHERE retired_at IS NULL;

-- ─────────── Per-tenant sequences ───────────
-- Generate human-readable IDs (SHA-YYYY-NNNNN, SHT-YYYY-NNNNN, SC-YYYY-NNNNN).
-- One counter per (tenant, year, kind). Pattern mirrors member_number_seq.
CREATE TABLE share_number_seq (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind        text NOT NULL,    -- 'account' | 'txn' | 'certificate'
  year        int  NOT NULL,
  last_value  int  NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, kind, year)
);

-- ─────────── RLS — tenant_id GUC enforcement ───────────
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY[
    'share_accounts', 'share_transactions', 'share_liens',
    'share_certificates', 'share_number_seq'
  ])
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

-- ─────────── Grants ───────────
GRANT SELECT, INSERT, UPDATE, DELETE ON
  share_accounts, share_transactions, share_liens,
  share_certificates, share_number_seq
TO nexus_app;
