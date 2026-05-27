# Fix the two ship-stoppers in the M-PESA B2C path

There are two load-bearing gaps in the phase-4 B2C work that mark the integration as "shipped" while the actual money-movement path is broken. Fix both in one PR.

## Gap 1 — `cmd/b2c-dispatcher` passes ciphertext to Daraja

`services/mpesa/cmd/b2c-dispatcher/main.go::loadCreds` (≈line 282) reads encrypted credential rows out of `mpesa_paybill_credentials` and returns the bytes unchanged. It then casts them to `string(...)` and feeds them to `daraja.AuthenticateForPaybill` (consumer key/secret) and `SecurityCredentialEncoder.Encode` (initiator password). Daraja receives ciphertext where it expects plaintext credentials and the call fails with `Invalid Authentication` (or worse, succeeds at the HTTP layer with bytes Safaricom rejects on the backend). The function comment openly acknowledges the gap — "Phase 5 will wire the Sealer in" — but the binary boots and pretends to work in the meantime. This is a critical bug, not a deferral; until it is fixed, no B2C disbursement can succeed in any environment.

The infrastructure to fix it already exists: `services/mpesa/internal/crypto/envelope.go::Sealer` exposes `NewSealer(activeID, key)` and `Decrypt(ciphertext) ([]byte, error)`, the `MPESA_KMS_MASTER_KEY` env var is already plumbed through `config.Load`, and the credential store's `ReadTx` already returns the `key_id` alongside the ciphertext so multi-key rotation is supported on day one.

## Gap 2 — reversal does not flip the loan back to `pending_disbursement`

`services/mpesa/internal/handler/b2c_reversal.go::Reverse` correctly persists the reversed outbound row, queues an `mpesa_b2c_reversal` workflow task for staff, and writes the audit entry. It deliberately does not touch the loan: the comment at line 20 defers that to "phase 5". The effect is books-vs-reality drift. If Safaricom reverses a B2C after we already received the result-success and called savings's `FinalizeDisbursement`, the loan stays in `LoanActive` with `principal_balance` set and a GL entry showing the cash left the M-PESA paybill — even though Safaricom has reversed that cash transfer. Every reversal in this state leaves a phantom-disbursed loan, the SACCO's books say the member owes money they never received, and only manual reconciliation closes the gap.

The savings side already has the matching forward-direction endpoint as the template: `services/savings/internal/handler/loan_finalize.go::FinalizeDisbursement` (`POST /internal/v1/loans/{loan_id}/finalize-disbursement`). It uses an `X-Internal-Token` gate, derives the tenant from the `loans.tenant_id` column, and is idempotent. Mirror that shape for the reverse direction. The mpesa-side `Reverse` handler then calls savings via the existing `savingsclient` after the mpesa-side tx commits (best-effort cross-service; the workflow task is the human safety net if the HTTP call fails).

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. Fix the two load-bearing gaps in the M-PESA B2C path that the phase-4 PR deferred. **This is one PR covering both fixes** — they ship together because the dispatcher's broken credentials path makes the reversal path also untestable end-to-end.
>
> **Scope**
>
> 1. Wire `crypto.Sealer` into `cmd/b2c-dispatcher` so credentials are decrypted before they go anywhere near Daraja.
> 2. Add a savings-side `POST /internal/v1/loans/{loan_id}/reverse-disbursement` endpoint and call it from the mpesa `b2c_reversal.go` handler so the loan transitions back to `pending_disbursement` (and the GL is unwound when the loan was already active).
> 3. End-to-end test that exercises both fixes against the sandbox flow.
>
> **Files you will read first**
>
> - `services/mpesa/cmd/b2c-dispatcher/main.go` (full — note the `dispatcher` struct at ≈line 119 and `loadCreds` at ≈line 282).
> - `services/mpesa/internal/crypto/envelope.go` — `Sealer`, `NewSealer`, `Decrypt`.
> - `services/mpesa/internal/store/credential_store.go` — `ReadTx` returns `(keyID string, ciphertext []byte, err error)`. `keyID` is the rotation hint.
> - `services/mpesa/internal/config/config.go` — confirm `MPESA_KMS_MASTER_KEY` is loaded and validated.
> - `services/mpesa/internal/handler/b2c_reversal.go` — full file. The block where the wf task is queued (≈line 95) is where the savings call goes.
> - `services/mpesa/internal/savingsclient/client.go` — `FinalizeDisbursement` is the template for the new `ReverseDisbursement` method.
> - `services/savings/internal/handler/loan_finalize.go` — `FinalizeDisbursement` (≈line 35). Mirror its shape, auth pattern, idempotency, and tenant-resolution.
> - `services/savings/internal/handler/loan.go::Disburse` (≈line 219) and `loan_disbursement_executor.go` — the forward path. Understand which fields the executor mutates (`status`, `principal_balance`, `interest_balance`, `disbursed_at`, etc.) so the reverse path can undo them.
> - `services/savings/internal/store/loan_store.go::loans` schema — `pending_disbursement` is the initial status (migration `0005_lending.up.sql:341`).
> - `services/savings/internal/handler/loan_repayment.go::postRepaymentToGLTx` (≈line 179) — the GL-outbox writer pattern. Reverse posts use the same outbox.
>
> ---
>
> ### Fix 1: Sealer in dispatcher
>
> **Steps**
>
> 1. In `services/mpesa/cmd/b2c-dispatcher/main.go`, add `sealer *crypto.Sealer` to the `dispatcher` struct. Construct it in `main()` from `cfg.KMSMasterKey` (the env-loaded bytes already in `config.Config`):
>    ```go
>    sealer, err := crypto.NewSealer(cfg.KMSActiveKeyID, cfg.KMSMasterKey)
>    if err != nil { logger.Error("sealer", "err", err); os.Exit(1) }
>    d.sealer = sealer
>    ```
>    If `cfg.KMSActiveKeyID` does not exist as a config field yet, add it (`MPESA_KMS_ACTIVE_KEY_ID`, default `kms-dev-001`). The Sealer needs an active id for envelope headers, even with one key.
> 2. Rewrite `loadCreds` to call `d.sealer.Decrypt` on every ciphertext before returning:
>    ```go
>    func (d *dispatcher) loadCreds(ctx context.Context, tx pgx.Tx, paybillID uuid.UUID) (
>        consumerKey, consumerSecret, initiatorName, initiatorPassword string, err error,
>    ) {
>        loadOne := func(kind domain.CredentialKind) (string, error) {
>            _, ct, e := d.credStore.ReadTx(ctx, tx, paybillID, kind)
>            if e != nil {
>                return "", fmt.Errorf("read %s: %w", kind, e)
>            }
>            pt, e := d.sealer.Decrypt(ct)
>            if e != nil {
>                return "", fmt.Errorf("decrypt %s: %w", kind, e)
>            }
>            return string(pt), nil
>        }
>        if consumerKey, err = loadOne(domain.CredConsumerKey); err != nil { return }
>        if consumerSecret, err = loadOne(domain.CredConsumerSecret); err != nil { return }
>        if initiatorName, err = loadOne(domain.CredInitiatorName); err != nil { return }
>        if initiatorPassword, err = loadOne(domain.CredInitiatorPassword); err != nil { return }
>        return
>    }
>    ```
>    Remove the SCOPE NOTE comment block now that the gap is closed.
> 3. The Sealer must be lazily-constructed once per process. Add a small unit test `cmd/b2c-dispatcher/loadcreds_test.go` that puts encrypted bytes via `crypto.Sealer.Encrypt` into a test paybill's credential rows (use the existing `store.CredentialStore.PutTx` API) and asserts the dispatcher reads them back as plaintext.
> 4. **Mirror the same wiring in `cmd/server`** — the admin-facing HTTP server already constructs a Sealer for the credentials-create handlers; double-check no other binary still passes raw ciphertext to Daraja. The `cmd/distributor` and `cmd/reconciler` binaries do not currently use credentials (only the server + b2c-dispatcher do); confirm and leave them.
> 5. Update the dispatcher's startup log line to include `kms_active_key_id` so operators can confirm rotation in production.
>
> **Failure modes to handle explicitly**
>
> - A decryption failure on any of the four credentials must abort the dispatch attempt with a `failed` row (not a `sent` row). The current dispatcher already routes `loadCreds` errors via `errOut` → `MarkFailedTx`. Verify that path still works after the rewrite.
> - A paybill that was created before the Sealer was wired (or before key rotation) might carry ciphertext sealed with a key the current Sealer doesn't know about. `Decrypt` returns `ErrUnknownKeyID` in that case. Translate that to a clear error message on the failed row (`"credential sealed with unknown key kms-…"`) so an operator can re-seal manually. Add a test for this branch using a Sealer with a different key.
>
> ---
>
> ### Fix 2: reverse-disbursement endpoint on savings + call from mpesa
>
> **Savings side**
>
> 1. New file `services/savings/internal/handler/loan_reverse_disbursement.go`. Mirror `loan_finalize.go`:
>    - `func (h *LoanHandler) ReverseDisbursement(w http.ResponseWriter, r *http.Request)`.
>    - Internal-token auth via `internalTokenFromEnv()` (already a helper in `loan_finalize.go` — extract to `internal_auth.go` if you want both handlers to share it cleanly).
>    - Path: `POST /internal/v1/loans/{loan_id}/reverse-disbursement`.
>    - Body: `{ "mpesa_reversal_receipt": string, "reason": string }`.
>    - Tenant resolution: identical to `FinalizeDisbursement` — `SELECT tenant_id FROM loans WHERE id = $1`.
> 2. The handler logic (one `WithTenantTx`):
>    - `loan, err := h.Loans.GetTx(tx, loanID)`.
>    - **Branch on `loan.Status`:**
>      - `LoanPendingDisbursement` → idempotent no-op. The forward `FinalizeDisbursement` never ran. Write an audit row `loan.disbursement_reversed (no_op)` with the mpesa_reversal_receipt + reason. Return `{"status":"no_op","loan_id":...}`.
>      - `LoanActive` → reverse path. Walk the executor's effects backwards in this exact order:
>        1. Zero out the loan's running balances (`principal_balance=0`, `interest_balance=0`, `fees_balance=0`, `penalty_balance=0`) and the disbursement stamps (`disbursed_at=NULL`, `last_repayment_at=NULL`). The schedule rows (if any were generated by `ExecuteDisbursementTx`) get deleted — use `DELETE FROM loan_schedule WHERE loan_id = $1` (mirror the cleanup logic in `loan_disbursement_executor.go`'s rollback branch, or extract it into a `ReverseDisbursementTx(tx, loanID)` store method if it doesn't exist).
>        2. Flip `loans.status` back to `'pending_disbursement'`.
>        3. Post the reversing GL entry via the outbox: DR Cash at M-PESA paybill, CR Loans Receivable for the disbursed principal. Use the same `postLoanDisbursementToGLTx`-style writer but with debits and credits swapped, source_module `"mpesa"`, source_ref `"reverse:" + original_source_ref`. The accounting service's `(source_module, source_ref)` dedup makes this idempotent.
>        4. Write audit `loan.disbursement_reversed` with `{mpesa_reversal_receipt, reason, original_disbursement_txn_id}`.
>      - Any other status (`LoanInArrears`, `LoanClosed`, `LoanWrittenOff`, `LoanRestructured`) → return `409 Conflict` with `{error: "loan is past disbursement; reversal must be handled manually", current_status: "..."}`. A reversed disbursement on a loan that has already received repayments is a real-money corner case that must not auto-unwind. The workflow task on the mpesa side already gives staff the surface to handle it; don't try to be clever in code.
>    - On rollback errors, return the existing `httpx.ErrGLPostFailed` if the outbox insert blew up; otherwise wrap with `writeLoanAppErr`.
> 3. Register the route in `services/savings/internal/handler/routes.go` next to `FinalizeDisbursement`. Same auth bracket.
> 4. Tests in `loan_reverse_disbursement_test.go`:
>    - Happy path: seed an active loan with a known disbursement → call `/reverse-disbursement` → assert status flipped, balances zeroed, GL outbox row enqueued with reversing legs, audit row written.
>    - Idempotency: call twice → second call returns 200, second outbox row deduped by accounting on `(source_module, source_ref)`.
>    - No-op branch: loan still in `pending_disbursement` → status unchanged, no GL outbox row, audit row tagged `no_op`.
>    - Conflict branch: loan in `LoanInArrears` → 409, no state changes.
>    - Tenant isolation: cross-tenant loan id returns 404 (the SELECT on `loans.tenant_id` finds it but `WithTenantTx` later refuses; document the precise behaviour and pin it in the test).
>
> **mpesa side**
>
> 5. Add `ReverseDisbursement(ctx, loanID, mpesaReversalReceipt, reason string) error` to `services/mpesa/internal/savingsclient/client.go`. Mirror `FinalizeDisbursement` — same `X-Internal-Token` header, same JSON body, same error handling. If `MPESA_SAVINGS_URL` is empty (dev mode), no-op and return `nil` with a warn log.
> 6. In `services/mpesa/internal/handler/b2c_reversal.go::Reverse`, after the inner `WithTenantTx` commits successfully and after the existing `audit(...)` call, look up the original outbound row's `source_module` and `source_ref`. If `source_module == "savings.loan_disbursement"` (the value `loan_disbursement_executor.go` already uses) and `source_ref` parses as a UUID, call:
>    ```go
>    if h.SavingsClient != nil {
>        loanID, _ := uuid.Parse(sourceRef)
>        if err := h.SavingsClient.ReverseDisbursement(r.Context(), loanID,
>            env.Result.TransactionID, env.Result.ResultDesc); err != nil {
>            h.Logger.Error("loan reverse-disbursement", "loan_id", loanID, "err", err)
>            // Best-effort; the wf task is the human safety net.
>        }
>    }
>    ```
>    This call **happens after the mpesa tx commits**. Failure does not roll back the outbound's `reversed` state — staff resolve the gap via the queued `mpesa_b2c_reversal` workflow task.
> 7. The wf task's `Context` map gets a new field `loan_id` so the staff inbox UI can deep-link to the loan profile. Update the existing `CreateInstanceInput{Context: …}` call.
> 8. Remove the comment block at the top of `b2c_reversal.go` (lines 20–23) that said the loan rollback is "phase 5 scope" — the rollback now lives in the file.
>
> ---
>
> ### End-to-end test
>
> Add `services/mpesa/internal/handler/b2c_reversal_e2e_test.go` (a wide integration test, not a unit test):
>
> 1. Boot mpesa + savings + accounting against the test DB.
> 2. Create a paybill with sealed credentials.
> 3. Create a loan in `pending_disbursement` and queue a `mpesa_outbound_request` for it.
> 4. Run the dispatcher once (`--once`). Mock the Daraja endpoint to return success. Verify the dispatcher decrypts credentials cleanly (no `ErrBadCiphertext` in the failure path), submits the request, marks `sent`.
> 5. Mock a Daraja Result success callback → savings finalizes → loan flips to `active`.
> 6. Mock a Daraja Reversal callback → mpesa marks outbound `reversed` → wf task queued → mpesa calls savings `/reverse-disbursement` → loan flips back to `pending_disbursement` → reversing GL outbox row appears.
> 7. Assert the GL trial balance for the loan's accounts is back to zero (net effect of disbursement + reversal).
> 8. Re-deliver the same reversal payload → asserted-noop everywhere (idempotency end-to-end).
>
> ---
>
> ### Idempotency / safety rules
>
> - No table-shape changes in this PR.
> - The savings-side reverse migration is additive only (route registration + handler; no new tables). No `.up.sql` / `.down.sql` change is needed.
> - The dispatcher must not crash if `MPESA_KMS_MASTER_KEY` is unset in dev — print a clear error and exit non-zero before `processOne` is reached. Add a startup-time sanity check that round-trips a known plaintext through the Sealer (`sealer.Decrypt(sealer.Encrypt([]byte("ping")))`) before entering the poll loop; fail fast if the round-trip is broken.
> - The reversal flow must remain a 200 to Safaricom even when the savings call fails — Daraja must not be told to retry. The workflow task is the human signal.
> - `gofmt`, `go vet`, `go test ./services/mpesa/... ./services/savings/...` all green before opening the PR.
>
> ### Acceptance walkthrough
>
> 1. Disburse a loan to a sandbox MSISDN. The dispatcher logs `kms_active_key_id=kms-dev-001` on startup, then `b2c sent outbound_id=… msisdn=…`. The savings service logs `loan.finalize_disbursement` and the loan flips to `active`.
> 2. Trigger a reversal on the Daraja simulator for the same conversation_id. The mpesa logs `b2c reverse persisted` followed by `loan reverse-disbursement` (savings call). The loan flips back to `pending_disbursement`. `SELECT principal_balance, interest_balance, status FROM loans WHERE id = …` returns `(0, 0, 'pending_disbursement')`. The wf inbox shows the `mpesa_b2c_reversal` task with `loan_id` deep-link.
> 3. Replay the same reversal payload. Second call returns 200; second savings call is a 200-`no_op`; second outbox row dedupes on `(source_module, source_ref)`. State unchanged.
> 4. Manually break the Sealer (delete the credential row's ciphertext and reinsert raw plaintext bytes pretending to be ciphertext). Re-run the dispatcher. The row is `failed` with `decrypt initiator_password: bad ciphertext` (or similar) — no Daraja call was made.
>
> When done, paste the diff stat, the new route in `routes.go`, the sealer-wiring diff in `main.go`, and the e2e test output into the PR description.

---

## Why this shape

Both fixes ride on infrastructure that already exists. The Sealer is in place, the credentials store already returns `key_id`, the savings service already has the forward-direction template (`FinalizeDisbursement`) with the auth pattern, tenant resolution, and idempotency we need, and the mpesa-side `Reverse` handler already creates the workflow task and the audit row. The only missing pieces are the actual decryption call and the reverse-direction HTTP hop — both are small, surgical changes that close real money-movement bugs.

The decision to call savings best-effort after the mpesa tx commits (rather than wrapping both in some distributed tx ceremony) matches how every other cross-service write in this codebase works: the workflow task is the recovery surface when the HTTP call fails. Keeps the change minimal and consistent.

The 409 on loans that have already received repayments is the only intentional non-automation — the alternative is a reversal engine that unwinds repayment allocations across schedule rows, which is real complexity for an edge case that should be handled by a human looking at the books.