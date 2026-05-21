-- Stage 9 (Credit refactor): introduce a prepaid credit model for the
-- credit-bearing channels (sms + email). In-app is unaffected.
--
-- New objects:
--   * notification_credit_balances        — current balance + alert thresholds per (tenant, channel)
--   * notification_credit_ledger          — append-only history of every movement
--   * notification_credit_topup_requests  — tenant-submitted requests for the platform to fulfil
--   * notification_credit_pricing         — per-tenant price per credit (driven by sales agreement)
--   * notification_credit_adjustments     — corrections that require maker/checker approval
--   * platform_smtp_config / platform_sms_config — the shared driver credentials owned by the platform
--
-- The existing per-tenant notification_smtp_configs / notification_sms_configs
-- tables are left in place but become obsolete; downstream code stops
-- reading from them. A follow-up cleanup migration can drop them once
-- we're confident nothing else references them.
--
-- The `blocked` value on notification_status is added in a separate
-- migration so it can be used right away (Postgres forbids referencing
-- a newly-added enum value within the same transaction).

-- ─────────── Enums ───────────

CREATE TYPE notification_credit_movement AS ENUM (
    'topup',
    'consumption',
    'adjustment',
    'expiry',
    'refund'
);

CREATE TYPE notification_topup_status AS ENUM (
    'pending',
    'fulfilled',
    'rejected',
    'cancelled'
);

CREATE TYPE notification_adjustment_status AS ENUM (
    'pending_approval',
    'approved',
    'rejected'
);

-- ─────────── Balances ───────────

CREATE TABLE notification_credit_balances (
    tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel                 notification_channel NOT NULL,
    balance                 integer NOT NULL DEFAULT 0 CHECK (balance >= 0),
    low_balance_threshold   integer NOT NULL DEFAULT 0 CHECK (low_balance_threshold >= 0),
    low_balance_alerted_at  timestamptz,
    zero_balance_alerted_at timestamptz,
    last_topup_at           timestamptz,
    last_topup_credits      integer,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, channel),
    CONSTRAINT credit_balances_channel_check CHECK (channel IN ('sms', 'email'))
);
ALTER TABLE notification_credit_balances ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_credit_balances FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_credit_balances ON notification_credit_balances
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_credit_balances TO nexus_app;

-- ─────────── Ledger ───────────

CREATE TABLE notification_credit_ledger (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel         notification_channel NOT NULL,
    movement_type   notification_credit_movement NOT NULL,
    credits         integer NOT NULL,           -- positive for topup/refund, negative for consumption
    balance_after   integer NOT NULL CHECK (balance_after >= 0),
    notification_id uuid REFERENCES notifications(id) ON DELETE SET NULL,
    delivery_id     uuid REFERENCES notification_deliveries(id) ON DELETE SET NULL,
    reference       text,                       -- invoice/PO number for top-ups
    actioned_by     uuid,                       -- platform admin for top-ups; NULL for system actions
    notes           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT credit_ledger_channel_check CHECK (channel IN ('sms', 'email'))
);
CREATE INDEX notification_credit_ledger_tenant_channel_idx
    ON notification_credit_ledger (tenant_id, channel, created_at DESC);
CREATE INDEX notification_credit_ledger_movement_idx
    ON notification_credit_ledger (tenant_id, movement_type, created_at DESC);
ALTER TABLE notification_credit_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_credit_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_credit_ledger ON notification_credit_ledger
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT ON notification_credit_ledger TO nexus_app;

-- ─────────── Top-up requests (tenant submits → platform fulfils) ───────────

CREATE TABLE notification_credit_topup_requests (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel               notification_channel NOT NULL,
    credits_requested     integer NOT NULL CHECK (credits_requested > 0),
    status                notification_topup_status NOT NULL DEFAULT 'pending',
    requested_by          uuid,
    requested_at          timestamptz NOT NULL DEFAULT now(),
    fulfilled_by          uuid,
    fulfilled_at          timestamptz,
    fulfillment_ledger_id uuid REFERENCES notification_credit_ledger(id) ON DELETE SET NULL,
    notes                 text,
    rejection_reason      text,
    CONSTRAINT topup_channel_check CHECK (channel IN ('sms', 'email'))
);
CREATE INDEX notification_credit_topup_requests_status_idx
    ON notification_credit_topup_requests (tenant_id, status, requested_at DESC);
ALTER TABLE notification_credit_topup_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_credit_topup_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_topup_requests ON notification_credit_topup_requests
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_credit_topup_requests TO nexus_app;

-- ─────────── Pricing (per tenant per channel) ───────────

CREATE TABLE notification_credit_pricing (
    tenant_id        uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel          notification_channel NOT NULL,
    price_per_credit numeric(12,4) NOT NULL DEFAULT 0 CHECK (price_per_credit >= 0),
    currency_code    char(3) NOT NULL DEFAULT 'KES',
    updated_at       timestamptz NOT NULL DEFAULT now(),
    updated_by       uuid,
    PRIMARY KEY (tenant_id, channel),
    CONSTRAINT pricing_channel_check CHECK (channel IN ('sms', 'email'))
);
ALTER TABLE notification_credit_pricing ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_credit_pricing FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_credit_pricing ON notification_credit_pricing
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_credit_pricing TO nexus_app;

-- ─────────── Adjustments (maker/checker) ───────────

CREATE TABLE notification_credit_adjustments (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    channel           notification_channel NOT NULL,
    credits           integer NOT NULL,         -- positive or negative
    reason            text NOT NULL,
    status            notification_adjustment_status NOT NULL DEFAULT 'pending_approval',
    requested_by      uuid NOT NULL,
    requested_at      timestamptz NOT NULL DEFAULT now(),
    approved_by       uuid,
    approved_at       timestamptz,
    rejected_by       uuid,
    rejected_at       timestamptz,
    rejection_reason  text,
    applied_ledger_id uuid REFERENCES notification_credit_ledger(id) ON DELETE SET NULL,
    CONSTRAINT adjustment_channel_check CHECK (channel IN ('sms', 'email')),
    CONSTRAINT adjustment_credits_nonzero CHECK (credits <> 0),
    -- Maker/checker: the approver must differ from the requester. We
    -- enforce this only on the row level via a check on approver+req.
    CONSTRAINT adjustment_distinct_actors CHECK (
        approved_by IS NULL OR approved_by <> requested_by
    )
);
CREATE INDEX notification_credit_adjustments_status_idx
    ON notification_credit_adjustments (tenant_id, status, requested_at DESC);
ALTER TABLE notification_credit_adjustments ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_credit_adjustments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_credit_adjustments ON notification_credit_adjustments
    USING (tenant_id = current_tenant_id())
    WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON notification_credit_adjustments TO nexus_app;

-- ─────────── Platform-level shared drivers ───────────
--
-- Singletons (id = 1) — platform-scoped, NOT tenant-scoped. RLS is
-- left disabled here; access control is enforced at the HTTP layer
-- via the platform-admin role check.

CREATE TABLE platform_smtp_config (
    id                  integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    host                text NOT NULL DEFAULT '',
    port                integer NOT NULL DEFAULT 1025,
    encryption          text NOT NULL DEFAULT 'none' CHECK (encryption IN ('none','starttls','tls')),
    username            text NOT NULL DEFAULT '',
    password_enc        text NOT NULL DEFAULT '',
    from_address        text NOT NULL DEFAULT 'no-reply@nexussacco.local',
    from_name           text NOT NULL DEFAULT 'nexusSacco',
    is_enabled          boolean NOT NULL DEFAULT false,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    updated_by          uuid
);
GRANT SELECT, INSERT, UPDATE ON platform_smtp_config TO nexus_app;
INSERT INTO platform_smtp_config (id) VALUES (1);

CREATE TABLE platform_sms_config (
    id                  integer PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    provider            text NOT NULL DEFAULT 'mock' CHECK (provider IN ('mock','sandbox','production')),
    username            text NOT NULL DEFAULT 'sandbox',
    api_key_enc         text NOT NULL DEFAULT '',
    sender_id           text NOT NULL DEFAULT '',
    rate_per_minute     integer NOT NULL DEFAULT 600,
    webhook_secret_enc  text NOT NULL DEFAULT '',
    is_enabled          boolean NOT NULL DEFAULT false,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    updated_by          uuid
);
GRANT SELECT, INSERT, UPDATE ON platform_sms_config TO nexus_app;
INSERT INTO platform_sms_config (id) VALUES (1);

-- ─────────── Delivery: track BLOCKED reason ───────────
--
-- The `blocked` status itself lands in migration 0010 so we can use it
-- in code immediately. For now we just add the column that records
-- *why* a delivery was blocked.

ALTER TABLE notification_deliveries
    ADD COLUMN blocked_reason text;

-- ─────────── Seeds: one balance + pricing row per (tenant, channel) ───────────

INSERT INTO notification_credit_balances (tenant_id, channel, balance, low_balance_threshold)
SELECT t.id, ch.channel::notification_channel, 0, 0
FROM tenants t
CROSS JOIN (VALUES ('sms'), ('email')) AS ch(channel)
ON CONFLICT DO NOTHING;

INSERT INTO notification_credit_pricing (tenant_id, channel, price_per_credit, currency_code)
SELECT t.id, ch.channel::notification_channel, 0, COALESCE(t.currency_code, 'KES')
FROM tenants t
CROSS JOIN (VALUES ('sms'), ('email')) AS ch(channel)
ON CONFLICT DO NOTHING;
