-- Stamp application-fee payments onto the new counterparty's ledger.
--
-- When a membership application carries application_fee_payments
-- rows and is then materialised, we want those payments to surface
-- on the new counterparty's Member 360 ledger (Accounts →
-- Transactions). Pre-this-migration there was no mechanism for that
-- — the GL and the application both had the fee, but nothing copied
-- it onto the counterparty's receipt history.
--
-- The fix: at materialise time (and via a backfill below for
-- already-materialised cases), insert ONE synthetic receipt per
-- non-voided application_fee_payment. Each receipt carries:
--
--   • application_id           — the application it originated from
--   • application_payment_id   — the specific payment row (UNIQUE)
--   • posted_outside_till      — true; bypasses the till_session /
--                                 virtual_till constraint that
--                                 cash + non-cash receipts normally
--                                 honour
--   • status = 'posted'        — the underlying GL entry already
--                                 exists; this receipt is just a
--                                 ledger surface
--   • receipt_lines.posted_txn_id = the application_fee_payment's
--                                 journal_entry_id (we attach to
--                                 the existing JE, we don't post a
--                                 new one)
--
-- Idempotency: the UNIQUE partial index on application_payment_id
-- + the handler's WHERE NOT EXISTS check both guard against
-- duplicate stamps.

ALTER TABLE receipts
  ADD COLUMN IF NOT EXISTS application_id         uuid REFERENCES membership_applications(id),
  ADD COLUMN IF NOT EXISTS application_payment_id uuid REFERENCES application_fee_payments(id),
  ADD COLUMN IF NOT EXISTS posted_outside_till    boolean NOT NULL DEFAULT false;

CREATE INDEX IF NOT EXISTS receipts_application_id_idx
  ON receipts (application_id) WHERE application_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS receipts_application_payment_uidx
  ON receipts (application_payment_id) WHERE application_payment_id IS NOT NULL;

-- Relax receipts_check to add a third valid shape for
-- application-origin receipts. The two pre-existing branches
-- (cash-with-till + non-cash-with-virtual-till) stay unchanged.
ALTER TABLE receipts DROP CONSTRAINT IF EXISTS receipts_check;
ALTER TABLE receipts ADD CONSTRAINT receipts_check CHECK (
  (channel = 'cash'::receipt_channel
    AND till_session_id IS NOT NULL
    AND virtual_till_id IS NULL
    AND channel_ref IS NULL)
  OR
  (channel <> 'cash'::receipt_channel
    AND virtual_till_id IS NOT NULL
    AND till_session_id IS NULL
    AND channel_ref IS NOT NULL)
  OR
  -- Application-origin receipts: no till of any kind, channel_ref
  -- unconstrained (we null it for cash, copy it for non-cash).
  (posted_outside_till = true
    AND till_session_id IS NULL
    AND virtual_till_id IS NULL)
);

-- ─────────── Backfill ───────────
-- Stamp one receipt + one fee-line per non-voided + posted
-- application_fee_payment whose parent application has been
-- materialised. Skipped per-payment when the UNIQUE partial index
-- already has a row, so re-running the migration is safe.
--
-- Cash payments get channel_ref nulled (matches the existing
-- "cash has no ref" semantics on the rest of the receipts table;
-- the original ref stays on application_fee_payments for audit).
--
-- Serial format: R-APP-{application_no}-{seq:02d}, where seq is
-- per-application row_number ordered by posted_at ASC (the new
-- store method uses the same formula).
DO $$
DECLARE
  app           record;
  payment       record;
  seq           int;
  receipt_id    uuid;
  ref           text;
BEGIN
  FOR app IN
    SELECT a.id, a.tenant_id, a.application_no, a.materialized_counterparty_id
      FROM membership_applications a
     WHERE a.materialized_counterparty_id IS NOT NULL
       AND EXISTS (
         SELECT 1 FROM application_fee_payments p
          WHERE p.application_id = a.id
            AND p.voided_at IS NULL
            AND p.journal_entry_id IS NOT NULL
       )
  LOOP
    seq := 0;
    FOR payment IN
      SELECT p.id, p.amount, p.channel, p.channel_reference,
             p.value_date, p.posted_at, p.journal_entry_id, p.created_by,
             p.note
        FROM application_fee_payments p
       WHERE p.application_id = app.id
         AND p.voided_at IS NULL
         AND p.journal_entry_id IS NOT NULL
       ORDER BY p.posted_at ASC, p.created_at ASC
    LOOP
      seq := seq + 1;
      -- Idempotency: skip if a receipt already exists for this
      -- application_payment_id.
      IF EXISTS (SELECT 1 FROM receipts WHERE application_payment_id = payment.id) THEN
        CONTINUE;
      END IF;
      ref := payment.channel_reference;
      IF payment.channel = 'cash' THEN
        ref := NULL;
      END IF;
      INSERT INTO receipts (
        tenant_id, serial, counterparty_id, channel, channel_ref,
        channel_amount, value_date, narration, cashier_user_id,
        till_session_id, virtual_till_id, status,
        posted_at, posted_outside_till,
        application_id, application_payment_id
      ) VALUES (
        app.tenant_id,
        -- application_no is 'APP-YYYY-NNNNNN' so we strip the leading
        -- 'APP-' before splicing it back into the receipt serial; the
        -- spec's example R-APP-2026-000007-01 drops the redundant
        -- prefix.
        'R-APP-' || regexp_replace(app.application_no, '^APP-', '') || '-' || lpad(seq::text, 2, '0'),
        app.materialized_counterparty_id,
        payment.channel::receipt_channel, ref,
        payment.amount, payment.value_date,
        'Application fee · ' || app.application_no,
        payment.created_by,
        NULL, NULL, 'posted'::receipt_status,
        payment.posted_at, true,
        app.id, payment.id
      )
      RETURNING id INTO receipt_id;

      INSERT INTO receipt_lines (
        receipt_id, line_no, kind, amount, fee_code,
        narration, posted_txn_id, status, posted_at
      ) VALUES (
        receipt_id, 1, 'fee'::receipt_line_kind, payment.amount, 'membership_registration',
        payment.note, payment.journal_entry_id,
        'posted'::receipt_line_status, payment.posted_at
      );

      RAISE NOTICE 'backfilled fee receipts: application % → counterparty %, payment % amount %',
        app.application_no, app.materialized_counterparty_id, payment.id, payment.amount;
    END LOOP;
  END LOOP;
END $$;
