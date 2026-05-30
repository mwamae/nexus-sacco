-- DSID Phase 2.2 — STK Push tracking.
--
-- Records each STK Push initiation so the callback can be matched
-- back to its source request (standing-order processor, ad-hoc admin
-- push, member-portal pull, etc.). On callback success the row's
-- status flips to 'completed' and a synthetic mpesa_inbound_events
-- row is inserted to drive the existing distribution waterfall.

BEGIN;

CREATE TYPE stk_request_status AS ENUM (
  'pending',
  'sent',          -- Daraja accepted (response code 0); waiting on user
  'completed',     -- user approved + callback returned ResultCode 0
  'failed',        -- Daraja rejected synchronously OR callback non-zero
  'cancelled'      -- user cancelled on phone, or timed out per Safaricom
);

CREATE TABLE mpesa_stk_requests (
  id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  paybill_id                  uuid NOT NULL REFERENCES mpesa_paybills(id) ON DELETE RESTRICT,
  msisdn                      text NOT NULL,
  amount                      numeric(18,2) NOT NULL CHECK (amount > 0),
  account_reference           text NOT NULL,                     -- "BillRef" the SACCO ledger reads (member_no, account_no, …)
  transaction_desc            text NOT NULL DEFAULT 'Standing order',
  source_module               text NOT NULL,                     -- e.g. 'savings.standing_order'
  source_ref                  text NOT NULL,                     -- e.g. recurring_deposits.id
  originator_conversation_id  text,                              -- echoed back on callback
  merchant_request_id         text,                              -- Daraja returns on initiate
  checkout_request_id         text UNIQUE,                       -- primary correlation key on callback
  status                      stk_request_status NOT NULL DEFAULT 'pending',
  response_code               text,                              -- synchronous Daraja response code
  response_description        text,
  result_code                 text,                              -- async callback result code
  result_desc                 text,
  mpesa_receipt_number        text,                              -- on success
  raw_initiate_response       jsonb,
  raw_callback                jsonb,
  inbound_event_id            uuid REFERENCES mpesa_inbound_events(id) ON DELETE SET NULL,
  initiated_at                timestamptz NOT NULL DEFAULT now(),
  completed_at                timestamptz,
  UNIQUE (source_module, source_ref, account_reference, initiated_at)
);
CREATE INDEX mpesa_stk_requests_pending_idx
  ON mpesa_stk_requests (tenant_id, status, initiated_at DESC);
CREATE INDEX mpesa_stk_requests_source_idx
  ON mpesa_stk_requests (source_module, source_ref);

ALTER TABLE mpesa_stk_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE mpesa_stk_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_mpesa_stk_requests ON mpesa_stk_requests
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE ON mpesa_stk_requests TO nexus_app;

COMMIT;
