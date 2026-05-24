-- Late-fee capture for membership applications.
--
-- Pre-this-migration the application's fee fields
-- (fee_amount_paid, fee_payment_channel, fee_payment_reference,
-- fee_payment_date, fee_status) were written exactly once at INSERT
-- and never updated. That meant an officer could capture a fee at
-- create time or never — there was no way to record a payment that
-- arrived after the application was submitted.
--
-- application_fee_payments is the per-payment audit table. The
-- denormalised columns on membership_applications BECOME aggregates
-- of the rows here:
--
--   fee_amount_paid       = SUM(amount) FILTER (WHERE voided_at IS NULL)
--   fee_payment_channel   = channel of the latest non-voided row
--   fee_payment_reference = reference of the latest non-voided row
--   fee_payment_date      = value_date of the latest non-voided row
--   fee_status            = derived from fee_amount_due vs paid
--
-- The handler does the recompute inside the same tx as the insert /
-- void so the aggregates never diverge from the rows below them.

CREATE TABLE IF NOT EXISTS application_fee_payments (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id     uuid NOT NULL
                          REFERENCES membership_applications(id) ON DELETE CASCADE,
  amount             numeric(18,2) NOT NULL CHECK (amount > 0),
  -- Free-text channel rather than an enum. The Collection Desk's
  -- receipt_channel and the savings deposit_channel are already two
  -- enums diverging by a few values; this table only needs a
  -- handler-validated string and isn't worth a third.
  channel            text NOT NULL,
  channel_reference  text,
  value_date         date NOT NULL,
  proof_doc_path     text,
  note               text,
  journal_entry_id   uuid,
  posted_at          timestamptz,
  voided_at          timestamptz,
  void_reason        text,
  -- created_by / voided_by are plain uuids (no FK to users) to
  -- mirror membership_applications.submitted_by — that table
  -- doesn't FK to users either, so this one shouldn't be the
  -- outlier that does.
  voided_by          uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid NOT NULL
);

CREATE INDEX IF NOT EXISTS application_fee_payments_app_posted_idx
  ON application_fee_payments (application_id, posted_at DESC);

-- Non-unique partial index supports the handler's idempotency lookup
-- (find a live row with the same channel+ref) without forcing the
-- write through a UNIQUE constraint — the handler returns the
-- existing row's id on 409 rather than relying on a constraint
-- violation.
CREATE INDEX IF NOT EXISTS application_fee_payments_chan_ref_idx
  ON application_fee_payments (tenant_id, channel, channel_reference)
  WHERE channel_reference IS NOT NULL AND voided_at IS NULL;

ALTER TABLE application_fee_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE application_fee_payments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_application_fee_payments ON application_fee_payments
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON application_fee_payments TO nexus_app;

-- Backfill — one row per existing application that already carries a
-- non-zero fee_amount_paid + a channel. Preserves history so the new
-- UI's payment-history table is non-empty for tenants who captured
-- fees pre-this-migration. Idempotent on re-run because we only
-- insert when no row exists yet for that application.
--
-- created_by uses submitted_by as the best available actor proxy —
-- the real cashier identity wasn't captured pre-this-migration, but
-- submitted_by is on every application by construction.
INSERT INTO application_fee_payments (
  tenant_id, application_id, amount, channel, channel_reference,
  value_date, proof_doc_path, note,
  journal_entry_id, posted_at,
  created_at, created_by
)
SELECT
  a.tenant_id, a.id, a.fee_amount_paid, a.fee_payment_channel, a.fee_payment_reference,
  COALESCE(a.fee_payment_date, a.submitted_at::date),
  a.fee_proof_doc_path, a.fee_shortfall_note,
  a.fee_journal_entry_id,
  CASE WHEN a.fee_journal_entry_id IS NOT NULL THEN a.submitted_at ELSE NULL END,
  a.submitted_at, a.submitted_by
FROM membership_applications a
WHERE a.fee_amount_paid > 0
  AND a.fee_payment_channel IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM application_fee_payments x
     WHERE x.application_id = a.id
  );
