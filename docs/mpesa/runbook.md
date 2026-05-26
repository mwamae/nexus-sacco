# M-PESA on-call runbook

Audience: on-call engineers + customer-success staff with
`mpesa:reconcile:run` permission. Assumes you can reach the
production DB via the privileged psql session and the admin UI as
a `sacco_admin` user.

## Quick reference

| Surface | Where |
|---|---|
| HTTP service | `:8087` (`/healthz`, `/readyz`, `/metrics`) |
| Distribution worker | `cmd/distributor` |
| B2C dispatcher | `cmd/b2c-dispatcher` |
| Daily reconciler | `cmd/reconciler -daily` |
| Soak harness | `cmd/reconciler -soak=N` |
| Admin UI | `/settings/mpesa` + `/accounting/mpesa-reconciliation` |
| Workflow inbox | `/approvals` (`mpesa_unallocated_reconciliation`, `mpesa_b2c_reversal`, `mpesa_reconciliation_diff`) |

Every log line + Daraja callback should carry `paybill_id`,
`tenant_id`, and `mpesa_receipt_number` (when present). When you
can't find a row, grep audit_log first — the `mpesa.*` action names
are the canonical breadcrumb trail.

---

## Go-live checklist for a new tenant

Before flipping a tenant to live M-PESA:

1. **Register the paybill** via `POST /v1/mpesa/paybills` or the
   Settings UI. Confirm the row exists:
   ```sql
   SELECT id, label, shortcode, purpose, environment, status, is_default
     FROM mpesa_paybills
    WHERE tenant_id = 'TENANT_UUID';
   ```
2. **Rotate credentials** via Settings → M-PESA → Rotate creds.
   Required kinds depend on purpose:
   - C2B-only: `consumer_key`, `consumer_secret`, `passkey`
   - B2C-only: `consumer_key`, `consumer_secret`, `initiator_name`, `initiator_password`
   - Both: all five
3. **Test auth** via the Settings UI's Test-auth button. Must
   return `{ok: true}` before going live. If it returns `ok: false`,
   re-check the consumer_key/secret and that the paybill is
   activated on the Daraja portal.
4. **Copy the four Daraja URLs** from Settings → Daraja portal URLs
   into the Daraja portal:
   - Validation URL (C2B)
   - Confirmation URL (C2B)
   - Result URL (B2C)
   - Timeout URL (B2C)
5. **Trigger a sandbox test transaction** via the Daraja simulator.
   Watch the Recent traffic panel — the row should land within ~3
   seconds with `status='distributed'`.
6. **Set `is_default=true`** on the B2C paybill ONCE per tenant so
   the loan-disburse picker resolves it automatically:
   ```sql
   UPDATE mpesa_paybills SET is_default = true
    WHERE id = 'PAYBILL_UUID' AND purpose IN ('disbursement','both');
   ```
7. **Confirm allow-list** — production `MPESA_TRUSTED_IPS` must
   contain Safaricom's webhook IPs. Empty value in production is
   refused at boot.
8. **Confirm cert pinning** — `internal/daraja/certs/production.pem`
   must contain a real Safaricom chain (NOT the `BEGIN PLACEHOLDER`
   stub). `MPESA_FORCE_SANDBOX=false` in production env.
9. **Schedule the reconciler** at 02:00 tenant-local via cron or
   your scheduler-of-choice:
   ```sh
   0 2 * * *  /usr/local/bin/mpesa-reconciler -daily
   ```
10. **Run a 24-hour soak** to validate the pipeline end-to-end (see
    the Soak section below).

---

## Rotate Daraja credentials

When Safaricom rotates the consumer key/secret (typically annually,
or after a portal-side regeneration):

1. Get the new values from the Daraja portal: Settings → API
   Credentials → "Regenerate" or "Reveal".
2. In the admin UI: Settings → M-PESA paybills → click "Rotate
   creds" on the affected row.
3. Paste each new value into its slot. Leave a field blank to
   keep the current value (e.g. when ONLY the consumer_secret
   rotated).
4. Click Save. The UI fires one `POST .../credentials` per
   non-empty field; each writes a fresh ciphertext + key_id and
   resets the in-memory OAuth token cache the next time the
   dispatcher runs.
5. Click "Test auth" on the same row. Must return `ok: true`
   before any cash motion through that paybill.
6. Confirm the rotation lands in audit_log:
   ```sql
   SELECT action, target_id, metadata, created_at
     FROM audit_log
    WHERE action = 'mpesa.credential_rotated'
    ORDER BY created_at DESC LIMIT 5;
   ```

If `Test auth` fails after a rotation, check the dispatcher's
on-disk OAuth cache hasn't pinned the previous token — the
`Invalidate` call lives on the daraja.Client; restarting the
distributor + b2c-dispatcher purges it.

---

## Manually reconcile an unallocated payment

When a payment lands without a resolver match (`resolved_via =
'unallocated'`), the webhook handler queues an
`mpesa_unallocated_reconciliation` workflow task. Staff with
`mpesa:reconcile:run` can resolve it.

### Diagnose

Find the row:

```sql
SELECT e.id, e.transaction_id, e.amount, e.msisdn, e.bill_ref,
       e.received_at, e.status, p.shortcode
  FROM mpesa_inbound_events e
  JOIN mpesa_paybills p ON p.id = e.paybill_id
 WHERE e.resolved_via = 'unallocated'
   AND e.status = 'received'
 ORDER BY e.received_at DESC;
```

### Common causes

| Symptom | Likely cause | Fix |
|---|---|---|
| `bill_ref` is empty | Customer didn't type an account number | Phone-based fallback — confirm `mpesa_paybills.allow_msisdn_fallback = true` for the paybill, then re-resolve |
| `bill_ref` looks like a typo of a real member_no | Customer typo | Manually pick the correct member in the workflow inbox; submit "match to member" |
| `bill_ref` matches a member_no that was renamed | The member was re-numbered after the payment landed | Same as above — manually pick the (new) member |
| Customer paid by mistake | Wrong paybill / refund needed | Mark "refund to sender" in the inbox; phase 7 ships the auto-refund B2C |

### Resolve

1. Open `/approvals` and find the `mpesa_unallocated_reconciliation`
   task. The Summary line carries the amount + MSISDN.
2. Use the side panel to pick the right member by member_no,
   cp_number, or by searching contact phone.
3. Submit. The handler re-runs distribution with the staff-chosen
   member id; the splits get applied via the same phase-3 +
   phase-3.5 pipeline as if the resolver had matched on its own.

If the workflow task is missing (e.g. workflow service was down
when the webhook landed):

```sql
-- Manually trigger reconciliation by clearing the resolver_via
-- field so the next distributor pass treats it as fresh.
UPDATE mpesa_inbound_events
   SET status = 'received', resolved_via = NULL, resolved_member_id = NULL
 WHERE id = 'EVENT_UUID';
-- Then run the distributor manually:
-- $ cmd/distributor -once
```

---

## Reversal that landed on a closed loan

Safaricom can reverse a B2C disbursement up to 7 days after the
fact. When the original loan is already `active` (or worse, has
been settled in the meantime), the reversal needs human judgement:
do we re-issue the disbursement, or do we cancel the loan?

### Diagnose

```sql
SELECT o.id, o.source_ref, o.amount, o.msisdn, o.status,
       l.loan_no, l.status AS loan_status,
       l.principal_disbursed, l.principal_balance
  FROM mpesa_outbound_requests o
  LEFT JOIN loans l ON l.id::text = o.source_ref
 WHERE o.status = 'reversed';
```

### Decision tree

1. **Loan status `pending_disbursement`** — the original disburse
   never completed. Simplest path: cancel the loan + delete the
   pending disbursement. Staff resubmits via the loan handler.

2. **Loan status `active`** — the disbursement DID land + we
   applied it, but Safaricom later reversed (typically a fraud
   recall or a member-initiated reversal). Two sub-cases:

   a. **Member hasn't drawn down yet** (loan_balances unchanged):
      Run the reverse-disburse flow. SQL:
      ```sql
      -- Mark the loan back to pending_disbursement; the savings
      -- finalize-reversal endpoint (phase 7) handles the GL.
      UPDATE loans SET status = 'pending_disbursement'
       WHERE id = 'LOAN_UUID';
      -- Manually flip the outbound row so a retry can use the
      -- same source_ref.
      UPDATE mpesa_outbound_requests
         SET status = 'failed',
             result_desc = 'reversed; awaiting staff retry'
       WHERE id = 'OUTBOUND_UUID';
      ```
      Then re-submit via the loan-disburse modal.

   b. **Member has drawn down** (interest accrued, partial
      repayments made): this is a recovery case. Open a
      `loan_settle` workflow task with the disputed amount; the
      reversal becomes a write-off candidate. Loop in finance.

3. **Loan status `settled` or `written_off`** — the loan closed
   between disbursement and reversal. Don't unwind. Treat the
   reversal as an unexpected credit:
   ```sql
   -- Park the cash in clearing pending finance review.
   UPDATE mpesa_outbound_requests
      SET status = 'reversed',
          result_desc = 'received reversal on closed loan'
    WHERE id = 'OUTBOUND_UUID';
   ```
   Open a `journal_reversal` workflow + post a manual entry
   debiting clearing, crediting whatever account the member's
   accountant assigns.

The reversal task lands in `mpesa_b2c_reversal` in the workflow
inbox — staff should resolve through the UI when possible. The SQL
above is for cases where the wf task is missing or the UI is
unavailable.

---

## Backfill when the distributor is down

If `cmd/distributor` was offline for a window (deploy lag, DB
maintenance, etc), inbound events pile up with `status='received'`
+ `posted_at IS NULL`. The recovery is straightforward but
non-obvious if you haven't done it before.

### Diagnose

```sql
SELECT count(*) AS stuck,
       min(received_at) AS oldest,
       max(received_at) AS newest
  FROM mpesa_inbound_events
 WHERE status = 'received' AND attempts < 6;
```

If `stuck` > 0 and `oldest` is more than ~10 minutes ago, the
distributor is behind.

### Recover

1. **Confirm the distributor is actually running**:
   ```sh
   ps aux | grep mpesa-distributor
   ```
   No process → start it. Crashing → tail logs for the panic.
2. **Manually drain via -once**:
   ```sh
   /usr/local/bin/mpesa-distributor -once
   ```
   The binary exits 0 after one full pass. Run it a few times
   until `stuck` drops to zero.
3. **Re-check stuck rows after each pass**. If `attempts >= 6`
   appears, those are HARD failures and the daemon won't retry.
   Investigate the `error_text`:
   ```sql
   SELECT id, transaction_id, error_text, attempts
     FROM mpesa_inbound_events
    WHERE status = 'received' AND attempts >= 6
    ORDER BY received_at;
   ```
   Common hard-fail causes:
   - Database deadlock with the orchestrator — usually clears on
     re-run.
   - Workflow definition missing for `mpesa_unallocated_reconciliation`
     — reapply migration 0003.
4. **Reset hard-failed rows** once you've fixed the underlying
   cause:
   ```sql
   UPDATE mpesa_inbound_events
      SET attempts = 0, error_text = NULL, locked_at = NULL, locked_by = NULL
    WHERE id IN (...);
   ```
   Then re-run the distributor.
5. **Run the reconciler** after the backfill so the
   `mpesa_statement_pulls.diff_count` doesn't carry yesterday's
   stuck rows:
   ```sh
   /usr/local/bin/mpesa-reconciler -daily
   ```

---

## 24-hour sandbox soak

The acceptance criteria for go-live: a 24-hour sandbox soak with
**zero unreconciled events**. Two harness modes support this.

### CI (PR validation)

`go test ./services/mpesa/...` runs `-soak=20` against the dev DB
in seconds. Any drift between the distributor + the reconciler
fails the test.

### Pre-production (24-hour acceptance)

Run the soak harness against the sandbox tenant with N=1000:

```sh
# In a tmux/screen session so it survives terminal disconnects.
/usr/local/bin/mpesa-reconciler -soak=1000 -soak-timeout=24h
```

The binary exits 0 when all 1000 events drain via the distributor
within the soak-timeout window. Non-zero exit means at least one
event stuck — investigate as a hard fail (see "Backfill" above).

Operator checklist:
1. Confirm sandbox env is isolated from production (no
   real-member fixtures).
2. Start the distributor + b2c-dispatcher in long-running mode.
3. Run the soak.
4. After the run, query the reconciler dashboard for any
   `mpesa_reconciliation_diffs` rows. Expected: 0.
5. Document the soak in the go-live ticket with the run id +
   exit code.

---

## Emergency: force everything off

If a paybill is somehow leaking traffic (e.g. wrong env, fraudulent
activity), you can disable it without redeploying:

```sql
UPDATE mpesa_paybills SET status = 'disabled' WHERE id = 'PAYBILL_UUID';
```

This stops:
- The webhook handler from accepting NEW inbound events (already-
  landed events still distribute).
- The b2c-dispatcher from dispatching new outbound rows for that
  paybill (lease query filters `status='active'`).

Re-enable with `status = 'active'` when ready. The Settings UI's
status badge reflects the change immediately.
