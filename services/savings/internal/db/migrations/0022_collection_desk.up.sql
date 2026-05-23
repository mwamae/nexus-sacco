-- Collection Desk — a single "cashier's counter" that handles every kind
-- of money-in (savings deposit + share purchase + loan repayment + fee)
-- against a single printed receipt. Replaces the scattered per-panel
-- entry surfaces (~11 buttons across deposits/shares/loans panels).
--
-- Three new tables:
--   virtual_tills          — one per (tenant, non-cash channel), auto-provisioned
--                            on first use. Reconciles to 0 against the channel's
--                            external statement at EOD; no opening float.
--   receipts               — the header row, one per cashier action.
--                            UNIQUE (tenant_id, channel, channel_ref) on
--                            non-cash channels = hard idempotency.
--   receipt_lines          — N lines per receipt, one per money-in kind.
--                            Each line gets its own approval instance + posts
--                            its own underlying txn (deposit/share/loan/fee).
--   receipt_serial_seq     — per-(tenant, till, date) counter for the
--                            "R-<till_code>-YYYYMMDD-NNNN" serial format.
--
-- Cross-service FKs:
--   counterparties           (member service)  — receipt.counterparty_id
--   till_sessions            (accounting svc)  — receipt.till_session_id (cash only)
--   users                    (identity service) — cashier_user_id, voided_by
-- All tables live in the same Postgres DB, so the FKs work; RLS-scoped
-- on tenant_id like every other savings table.

BEGIN;

-- ─── Enums ────────────────────────────────────────────────
CREATE TYPE receipt_status AS ENUM (
  'draft',     -- header created but no lines posted yet (transient)
  'posted',    -- every line has reached a terminal state (posted/declined/voided)
  'voided'     -- the whole receipt was voided post-creation
);

CREATE TYPE receipt_line_kind AS ENUM (
  'savings_deposit',
  'share_purchase',
  'loan_repayment',
  'fee',
  'welfare'       -- placeholder for future welfare catalog; treated as fee today
);

CREATE TYPE receipt_line_status AS ENUM (
  'pending',   -- approval queued, not yet executed
  'posted',    -- underlying ledger txn written
  'declined',  -- approval rejected; receipt continues with remaining lines
  'voided'     -- line-level void (per the per-line void decision in the plan)
);

CREATE TYPE receipt_channel AS ENUM (
  'cash', 'mpesa', 'airtel_money', 'bank_transfer', 'cheque', 'standing_order'
);

-- ─── Virtual tills ────────────────────────────────────────
-- One row per (tenant, non-cash channel). Created lazily on first use
-- via INSERT ... ON CONFLICT DO NOTHING from the receipt-create path.
-- GL account code defaults to the per-channel suspense account; tenant
-- can override later if their chart-of-accounts differs.
CREATE TABLE virtual_tills (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  channel         receipt_channel NOT NULL,
  gl_account_code text NOT NULL,
  display_name    text NOT NULL,
  is_active       boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, channel),
  CHECK (channel <> 'cash') -- cash uses real till_sessions, never a virtual till
);

ALTER TABLE virtual_tills ENABLE ROW LEVEL SECURITY;
CREATE POLICY virtual_tills_tenant ON virtual_tills
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- ─── Receipt-serial sequence per (tenant, till, date) ─────
-- Keyed by virtual_till_id for non-cash, till_session_id for cash. We
-- store the till_code text on the seq row to avoid joining at format
-- time; the serial format is R-<till_code>-YYYYMMDD-NNNN.
CREATE TABLE receipt_serial_seq (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  till_code   text NOT NULL,
  date_key    date NOT NULL,
  last_no     int  NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, till_code, date_key)
);

ALTER TABLE receipt_serial_seq ENABLE ROW LEVEL SECURITY;
CREATE POLICY receipt_serial_seq_tenant ON receipt_serial_seq
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- ─── Receipts (header) ────────────────────────────────────
-- Exactly one of (till_session_id, virtual_till_id) is set:
--   cash channel → till_session_id (FK to accounting.till_sessions)
--   else         → virtual_till_id
CREATE TABLE receipts (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
  serial            text NOT NULL,                    -- "R-T1-20260523-0007"
  counterparty_id   uuid NOT NULL REFERENCES counterparties(id) ON DELETE RESTRICT,
  channel           receipt_channel NOT NULL,
  channel_ref       text,                              -- M-Pesa code, cheque no, etc.; NULL for cash
  channel_amount    numeric(20, 2) NOT NULL CHECK (channel_amount > 0),
  value_date        date NOT NULL DEFAULT current_date,
  narration         text,
  cashier_user_id   uuid NOT NULL,                     -- FK to users (identity svc, shared DB)
  till_session_id   uuid REFERENCES till_sessions(id) ON DELETE RESTRICT,
  virtual_till_id   uuid REFERENCES virtual_tills(id) ON DELETE RESTRICT,
  status            receipt_status NOT NULL DEFAULT 'draft',
  pdf_document_id   uuid,                              -- FK to pdf_documents (notification svc)
  voided_at         timestamptz,
  voided_by         uuid,
  void_reason       text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  posted_at         timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, serial),
  -- Hard idempotency: same channel + ref in the same tenant = same receipt.
  -- Cash skips this because cash receipts have no ref.
  CONSTRAINT receipts_channel_ref_unique
    UNIQUE NULLS NOT DISTINCT (tenant_id, channel, channel_ref)
    DEFERRABLE INITIALLY IMMEDIATE,
  CHECK (
    (channel = 'cash' AND till_session_id IS NOT NULL AND virtual_till_id IS NULL AND channel_ref IS NULL)
    OR
    (channel <> 'cash' AND virtual_till_id IS NOT NULL AND till_session_id IS NULL AND channel_ref IS NOT NULL)
  )
);

ALTER TABLE receipts ENABLE ROW LEVEL SECURITY;
CREATE POLICY receipts_tenant ON receipts
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE INDEX receipts_counterparty_idx       ON receipts(counterparty_id, created_at DESC);
CREATE INDEX receipts_till_session_idx       ON receipts(till_session_id) WHERE till_session_id IS NOT NULL;
CREATE INDEX receipts_virtual_till_idx       ON receipts(virtual_till_id) WHERE virtual_till_id IS NOT NULL;
CREATE INDEX receipts_cashier_date_idx       ON receipts(cashier_user_id, value_date DESC);
CREATE INDEX receipts_status_idx             ON receipts(status, tenant_id);

-- ─── Receipt lines ─────────────────────────────────────────
CREATE TABLE receipt_lines (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  receipt_id        uuid NOT NULL REFERENCES receipts(id) ON DELETE CASCADE,
  line_no           int  NOT NULL,             -- 1, 2, 3 within a receipt
  kind              receipt_line_kind NOT NULL,
  amount            numeric(20, 2) NOT NULL CHECK (amount > 0),
  -- Target — exactly one of these is set depending on kind. Validated
  -- in Go land at INSERT time, not via SQL CHECK (FK targets cross
  -- service boundaries; the per-kind FK semantics live in the store).
  target_account_id uuid,                       -- deposit_accounts.id, share_accounts.id, loans.id
  fee_code          text,                       -- 'loan_origination', 'membership', etc.
  narration         text,
  -- Approval + posting linkage (filled progressively).
  approval_id       uuid,                       -- pending_approvals.id; NULL means no approval needed
  posted_txn_id     uuid,                       -- the underlying ledger txn id, set on post
  status            receipt_line_status NOT NULL DEFAULT 'pending',
  voided_at         timestamptz,
  voided_by         uuid,
  void_reason       text,
  created_at        timestamptz NOT NULL DEFAULT now(),
  posted_at         timestamptz,
  UNIQUE (receipt_id, line_no)
);

ALTER TABLE receipt_lines ENABLE ROW LEVEL SECURITY;
CREATE POLICY receipt_lines_tenant ON receipt_lines
  USING (receipt_id IN (SELECT id FROM receipts WHERE tenant_id = current_tenant_id()))
  WITH CHECK (receipt_id IN (SELECT id FROM receipts WHERE tenant_id = current_tenant_id()));

CREATE INDEX receipt_lines_receipt_idx ON receipt_lines(receipt_id, line_no);
CREATE INDEX receipt_lines_approval_idx ON receipt_lines(approval_id) WHERE approval_id IS NOT NULL;
CREATE INDEX receipt_lines_target_idx   ON receipt_lines(kind, target_account_id) WHERE target_account_id IS NOT NULL;

-- ─── Seed virtual-till GL defaults (per-tenant suspense accounts) ─
-- The actual virtual_tills rows are created lazily on first use from
-- Go land; this is just documenting the default GL account codes the
-- factory will use. Tenant can override post-create.
--
-- Defaults:
--   mpesa          → 1020  (Mpesa Suspense)
--   airtel_money   → 1021  (Airtel Money Suspense)
--   bank_transfer  → 1030  (Bank Transfer Suspense)
--   cheque         → 1040  (Cheque Suspense)
--   standing_order → 1050  (Standing Order Suspense)
-- These match the existing till GL code convention (1010 = till float,
-- 1000 = vault, 2250 = variance) — they're co-located in the 10xx
-- "cash and equivalents" block.

COMMIT;
