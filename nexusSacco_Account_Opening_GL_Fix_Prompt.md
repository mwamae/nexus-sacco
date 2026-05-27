# Account-opening with money skips approvals, receipts, and GL — fix the missed path

## What I tested and found

The prior PR fixed the four named inline handlers (`Deposit`, `Withdraw`, `share.Purchase`, `loan_repayment.PostRepayment`) and the approval executor. **It did not touch the Open Account flow.** That's the path the user just hit: opening Eric Warui's BOSA account with a credit. Two production-relevant code paths share this gap.

### Path A — `POST /v1/deposit-accounts` (deposit.go::Open)

`services/savings/internal/handler/deposit.go:109 Open`. The handler accepts a single request that creates the account **and** posts the opening deposit. It:

1. Creates `deposit_accounts` row via `Deposits.OpenAccountTx`.
2. `OpenAccountTx` itself (in `services/savings/internal/store/deposit_store.go:94`) writes the `deposit_transactions` opening_balance row and bumps `deposit_accounts.current_balance` when `OpeningDeposit > 0`.
3. Returns the account + opening transaction.

What it does **not** do:

- **Never reads `toggles.Deposit`** — bypasses the approval gate the `Deposit` handler now checks. Opening with money sneaks past the maker-checker control that a same-day deposit to the same account would trigger.
- **Never calls `postDepositToGLTx`** — no `posting_outbox` row is written. No journal entry ever lands.
- **Never calls `receiptops.WriteTx`** — no receipt header / line. Invisible to Today's receipts.

So when Eric opens an account with KES 5,000 opening deposit: deposit_accounts ✓, deposit_transactions ✓, deposit_accounts.current_balance ✓, **approvals ✗, receipts ✗, journal entries ✗, Balance Sheet ✗, SASRA ✗**. Exactly what the user observed.

### Path B — Application materialisation BOSA opening

`services/member/internal/store/application_store.go:836` — the BOSA opening block inside `activateIndividualTx`. This runs when an application is approved and the SACCO has the `OpeningBosaAmount` > 0. Same shape: it INSERTs into `deposit_accounts` and `deposit_transactions` directly (bypassing the `OpenAccountTx` helper) and bumps cached balances. No receipt, no GL outbox, no approval check.

This path is hit on every materialise-individual-member approval where the applicant pledged an opening BOSA contribution. It's been silently dropping GL since the BOSA-FOSA restructure earlier this session.

### `services/finance/executor/deposit.go` — not the bug, confirms the design

The new `services/finance/executor` is a shared library module (`go.mod`) that the M-PESA C2B distributor uses to credit deposits. Its header explicitly says: "Adds funds to an existing deposit_accounts row (**no opening flow — that's a heavier handler**). Writes the deposit_transactions row + queues the GL outbox." So this path is correct — and it tells you the design intent: opening is supposed to be a "heavier handler" that does approval + receipt + GL on top of the bare `deposit_transactions` insert. That handler never got written. Path A is the gap.

### Why the previous fix missed it

The earlier prompt named four inline handlers — `Deposit`, `Withdraw`, `Purchase`, `PostRepayment`. Open isn't one of those four. It was treated as "account creation" (administrative, not money) when in fact it accepts an `OpeningDeposit` field that is exactly as cash-event-shaped as a `Deposit` call to the same account.

## Why this is structurally important to fix right

This is the same class of bug as the previous PR — a code path that moves money without going through the canonical seam. Patching it in isolation is fine, but the third time this kind of bug surfaces (we're now at three: collection-desk-only-receipts, executor-no-GL, open-account-no-GL) the right move is to **make it structurally impossible to write a money-moving handler that skips the seam.** The fix prompt below closes the immediate bug AND adds the analyzer rule that catches this class.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. The Open-deposit-account flow (`POST /v1/deposit-accounts`) accepts an `opening_deposit` field that moves money but skips approvals, receipts, and the GL outbox. The application-materialisation BOSA opening has the same gap. Fix both. Then add an analyzer rule that catches this class of bug going forward.
>
> **Files you will read first**
>
> - `services/savings/internal/handler/deposit.go:109 Open` — the broken inline handler.
> - `services/savings/internal/handler/deposit.go:372 Deposit` — the *correctly-fixed* inline handler for the existing-account case. Use this as the template.
> - `services/savings/internal/store/deposit_store.go:94 OpenAccountTx` — the store function that today writes both the account and the opening txn.
> - `services/savings/internal/handler/deposit_executors.go::ExecuteDepositTx` — the executor used by the approval path.
> - `services/savings/internal/handler/pending_approvals.go:593–614` — the `ApprovalKindDeposit` branch (already correctly wired).
> - `services/savings/internal/receiptops/` and `services/savings/internal/postingops/` — the seams the fix flows through.
> - `services/member/internal/store/application_store.go:780–870` — the BOSA opening block (also broken).
> - `tools/postingcheck/` — the existing analyzer; you'll extend it.
>
> ---
>
> ### Step 1: split `OpenAccountTx` into two — account creation, opening deposit deferred to the handler
>
> The current `OpenAccountTx` writes both the `deposit_accounts` row AND the opening `deposit_transactions` row. Split this:
>
> 1. Rename `OpenAccountTx(ctx, tx, in, accountNo) (*Account, *Transaction, error)` to `CreateAccountTx(ctx, tx, in, accountNo) (*Account, error)`. Remove the opening-deposit block (lines 138–157). Returns the new account in `'active'` status with zero balances.
> 2. Update the only existing caller (`deposit.go::Open`) to call the new `CreateAccountTx` and then compose the opening deposit as a separate step (Step 2).
> 3. Update the `OpenInput` struct comment to clarify: "OpeningDeposit no longer triggers a txn write from the store; the handler composes it via the same path the `Deposit` handler uses."
>
> ### Step 2: rewrite `deposit.go::Open` to compose Open + Deposit
>
> The handler now does two things in one HTTP call, with the second leg honouring approvals/receipts/GL exactly like the standalone `Deposit` endpoint:
>
> 1. **Phase 1 — account creation.** Always immediate, no approval, no GL. The account exists in `'active'` with zero balance. Insert audit row `deposit_account.opened`.
> 2. **Phase 2 — opening deposit (only when `OpeningDeposit > 0`).** Build a `DepositPayload` from the request (`AccountID = newly-created.ID, Amount = OpeningDeposit, Channel = OpeningChannel, …`) and run the **same** code path the `Deposit` handler runs at line 372:
>    - `toggles.Deposit == true` → queue a `pending_approval` of kind `ApprovalKindDeposit`. Also write a pending receipt + line through `receiptops.WriteTx` and attach the approval id. Return the response with `{account, pending_approval, opening_txn: null}`.
>    - `toggles.Deposit == false` → call `ExecuteDepositTx`, then `postDepositToGLTx`, then `receiptops.WriteTx` (posted line). Return `{account, opening_txn: result.Transaction}`.
> 3. **Both phases run inside a single `WithTenantTx`.** Account creation + the approval-queue-or-immediate-post commit together. If phase 2 errors, phase 1 rolls back — there's no half-opened account with no money.
> 4. Extract the deposit-half logic into a small helper `executeDepositInlineTx(ctx, tx, payload, userID, toggles) (result *DepositResult, pending *PendingApproval, receipt *Receipt, err error)` so `Deposit` (the existing handler) and `Open` (this handler) both call it. Refactor `Deposit` to call the same helper. **Two handlers, one money-moving path.** This is the structural fix — the bug can't recur on Open because the same code is on both routes.
> 5. Update the OpenAPI / response schema for `POST /v1/deposit-accounts`: it now returns one of:
>    - `{account, opening_txn?: Transaction}` (no opening deposit, or opening deposit immediate)
>    - `{account, pending_approval: PendingApproval}` (opening deposit queued)
>    The UI's "Open account" modal needs to distinguish these — render the same "Pending approval" banner the `Deposit` modal already shows when `pending_approval` is present.
>
> ### Step 3: fix the application-materialisation BOSA path
>
> `services/member/internal/store/application_store.go` around line 780–870 (the BOSA opening block inside `activateIndividualTx`) directly INSERTs into `deposit_accounts` + `deposit_transactions`. Replace with calls to the shared primitives:
>
> 1. Use `savings/store::DepositStore.CreateAccountTx` (the new function from Step 1) to create the BOSA account.
> 2. If `app.OpeningBosaAmount > 0`, post the deposit via the **same** `executeDepositInlineTx` helper from Step 2, but with `toggles.Deposit` **forced to false** — application activation is a maker-checker event in its own right (the application approval IS the deposit approval), so queuing another approval would be redundant. Document this in the call site.
> 3. Receipt: write a receipt row via `receiptops.WriteTx` tagged `source = "application_activation_bosa_opening"`.
> 4. GL: same `postingops.PostDepositTx` call as the inline path. Routes to the same `posting_outbox`.
>
> The cross-service dependency from `services/member` on `services/savings` already exists (the `application_store` reads BOSA product info). If the helper is internal to savings, expose it through `services/savings/pkg/openingdeposit/` — small package, one function: `PostOpeningDepositTx(ctx, tx, deps, in)`. Both savings's Open handler and member's application_store call it.
>
> ### Step 4: extend `tools/postingcheck` so this class of bug can't return
>
> The analyzer today catches "money-moving handler that doesn't call posting" by looking at handler functions in `handler/` packages. Extend the rules:
>
> 1. **Rule R-OPEN-1.** Flag any function whose name matches `Open*` or any function on a handler struct that reads a request field named `opening_*` / `OpeningDeposit` / `OpeningBalance` / `OpeningContribution` and does NOT (in the same function) call any of: `receiptops.WriteTx`, `postingops.Post*`, OR queue a `pending_approval`. The rule is "any HTTP-level function that observes an opening-money input must pass it through approval-or-receipt-or-GL — pick one." Skipping all three is the bug.
> 2. **Rule R-OPEN-2.** Flag any direct `INSERT INTO deposit_transactions` / `INSERT INTO share_transactions` / `INSERT INTO loan_transactions` SQL string in `*.go` files outside `services/savings/internal/store/` and `services/finance/executor/`. Those two packages are the *only* sanctioned writers; everywhere else must call into them. The application_store.go violation is the case study.
> 3. Add per-rule test fixtures under `tools/postingcheck/testdata/` that show the violating shape and the fixed shape. Update the README.
> 4. Make `make lint` fail on R-OPEN-1 / R-OPEN-2 violations.
>
> ### Step 5: backfill
>
> Existing Eric-Warui-style opening transactions in production already have orphan `deposit_transactions` rows with no `posting_outbox` row and no `receipts` row. The previous PR shipped `cmd/inline-receipt-backfill` for the regular-deposit case; extend it to also cover `txn_type = 'opening_balance'` rows:
>
> 1. The script reads `deposit_transactions WHERE txn_type = 'opening_balance' AND NOT EXISTS (SELECT 1 FROM receipts r WHERE …)` and:
>    - Writes the synthetic receipt + line in the INLINE virtual till.
>    - Optionally (behind `--gl-backfill`) writes a `posting_outbox` row with `source_module = 'savings.deposits.opening'` and `source_ref = transaction.id.String()`. The accounting dedup handles re-runs.
> 2. Document in the script header: "Run after the open-deposit fix lands. Idempotent. `--apply` writes receipts; `--gl-backfill` ALSO queues the missing GL rows."
>
> ### Acceptance walkthrough
>
> Setup: tenant with `toggles.Deposit = true` (default).
>
> 1. From Eric Warui's profile → Accounts → Open BOSA account, opening deposit KES 5,000, channel = MPESA, ref = test-12345.
> 2. UI shows "Account opened. Opening deposit pending approval." The account is visible on Eric's profile under Accounts with status active and balance 0.
> 3. `/collect/receipts` lists a receipt for Eric, status pending, line kind savings_deposit, amount 5,000.
> 4. As a checker, approve the pending approval. UI flips to "Approved. KES 5,000 posted." Account balance updates to 5,000.
> 5. `/accounting/journal-entries` shows a balanced JE: DR 1030 (M-PESA Cash) 5,000 / CR 2000 (Ordinary Savings) 5,000.
> 6. `/accounting/reports/balance-sheet` reflects the change.
> 7. SASRA `total_deposits` increased by 5,000.
> 8. Repeat with `toggles.Deposit = false` — same outcome, no pending step, JE posted inside the open tx, receipt written as `posted`.
> 9. Onboard a new member with `opening_bosa_amount = 10,000`. Approve the application. `/collect/receipts` shows the opening BOSA contribution; `/accounting/journal-entries` shows the matching JE; the member's BOSA account is active with balance 10,000.
> 10. Run `make lint`. Both new rules pass on the fixed code; introduce a one-line regression (delete the postingops call from `Open`) and re-run — `postingcheck` flags it.
>
> ### Idempotency / safety
>
> - The split of `OpenAccountTx` → `CreateAccountTx` + handler composition is a breaking API change for any non-handler caller. Grep confirms only `deposit.go::Open` calls it today; member's `application_store.go` does its own SQL. Update both at once.
> - Account number allocation (`nextSeqExport`) stays exactly where it is — no impact on the receipt unique-key invariants.
> - `gofmt`, `go vet`, full `go test` across savings + member + finance. Run `make lint` and confirm both new rules fire on the broken examples in `testdata/` and stay quiet on the fixed code.
> - The acceptance walkthrough must be runnable against the dev stack from the previous PR (accounting + posting-dispatcher up). If the dispatcher isn't running, the JE assertion will fail — call that out in the PR description so reviewers know to bring the stack up.
> - The backfill script's `--gl-backfill` flag stays default-OFF; auditor signs off before flipping it.
>
> When you're done, paste the diff stat, the new helper signature (`executeDepositInlineTx`), the two new analyzer rules (with passing + failing test fixtures), and the acceptance walkthrough output into the PR description.

---

## Why this shape

The previous PR closed the inline-handler gap for the four named cash-event endpoints. This one closes it for the fifth (Open) and for the application-materialisation path that mirrors the same shape. The extracted helper `executeDepositInlineTx` is the structural fix — the next person who adds a new money-moving deposit endpoint has exactly one function to call, and they can't write a path that skips it. The analyzer rules (R-OPEN-1, R-OPEN-2) are the wall against regression: they catch both the "opening-money input not routed through the seam" pattern and the "direct INSERT INTO money tables outside sanctioned packages" pattern. Together they make the bug structurally impossible to reintroduce without tripping CI.

The backfill is small and safe — receipts-only by default; GL backfill behind a flag because every prior orphan opening txn is a real accounting event that didn't reach the books, and the auditor wants to be in the room for that decision.