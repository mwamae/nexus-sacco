# Every cash event must produce a receipt and a GL post — fix two systemic gaps

## What I tested and found

I read each code path end-to-end. There are two distinct, systemic bugs hiding behind the user's report. Both are caused by the same architectural drift: the Collection Desk's receipt-line + approval-executor pipeline is treated as the canonical money-moving path, but every "inline" panel (Member 360 → Buy shares, → Deposit, → Withdraw, → Loan repayment) writes through a side door that the canonical pipeline does not police.

### Bug 1 — "Today's receipts" only shows Collection Desk transactions

Confirmed by tracing the read side and every write path:

- The **"Today's receipts"** page (`web/admin/src/pages/CollectionReceipts.tsx:52`) calls `listReceipts({till_session_id, value_date})` which hits `/v1/receipts` (`services/savings/internal/handler/collection_desk.go:619 ListReceipts`). That endpoint reads exclusively from the `receipts` + `receipt_lines` tables (migration `0022_collection_desk.up.sql:98`).
- **The Collection Desk path** (`collection_desk.go::CreateReceipt`) writes a `receipts` row with one or more `receipt_lines`, then for each line either posts inline or queues an approval that flips the line to `posted` on approve.
- **The Member 360 → Buy shares path** (`web/admin/src/components/MemberAccountsPanel.tsx:896` → `api/client.ts:2571 purchaseShares` → `POST /v1/share-accounts/by-counterparty/{cp_id}/purchase` → `services/savings/internal/handler/share.go:357 Purchase`) **never inserts into `receipts`**. It writes `share_transactions`, bumps `share_accounts.shares_held`, and (when approvals are off) queues a `posting_outbox` row. No receipt row ever exists.
- **The Member 360 → Deposit path** (`deposit.go:372 Deposit`, route `POST /v1/deposit-accounts/{account_id}/deposit`) — same shape. Writes `deposit_transactions`, updates `deposit_accounts.current_balance`, no `receipts` row.
- **Loan repayment** (`loan_repayment.go::PostRepayment`) — same shape. No `receipts` row.

So when a member buys shares from the profile, the underlying subledger row exists, but the "Today's receipts" page has no idea — the receipts table is empty for that event.

### Bug 2 — Approved purchases never reach the General Ledger

This is the more serious one. I confirmed it by reading the approval executor:

- The **inline / toggle-off** path in `share.go::Purchase` (line 422–431) calls `ExecuteSharePurchaseTx` **and then** `postSharePurchaseToGLTx`. That second call writes to `posting_outbox` inside the same tx. The dispatcher drains the outbox and posts to the GL. Trial balance moves. Good.
- The **toggle-on** path queues a `pending_approval`. When the checker approves, `pending_approvals.go:171 Approve` calls `executePayloadTx` which, for `ApprovalKindSharePurchase`, runs `h.Share.ExecuteSharePurchaseTx` (lines 620–633). **It never calls `postSharePurchaseToGLTx`.** No outbox row is written. The dispatcher has nothing to drain. The GL never sees the share purchase. The Balance Sheet, SASRA return, and every other report stays flat.

Same pattern for every other money kind: `ApprovalKindDeposit` (line 575) calls `ExecuteDepositTx` with no GL post; `ApprovalKindWithdrawal` (line 590), `ApprovalKindDepositTransfer` (line 605), `ApprovalKindShareTransfer` (line 635), `ApprovalKindShareBonus` (line 650), `ApprovalKindShareLien` (line 665), `ApprovalKindLoanDisbursement` (line 680), `ApprovalKindLoanRepayment`, `ApprovalKindLoanSettle`, `ApprovalKindLoanReverse`, `ApprovalKindLoanWriteoff`, `ApprovalKindLoanReschedule`, `ApprovalKindLoanMoratorium`, `ApprovalKindLoanSettlementDiscount` — none of them post to GL.

The **only** approval executor that posts to GL is `ApprovalKindFeePosting` / `ApprovalKindWelfarePosting` (line 806), which calls `h.Collection.postFeeLineTx` (which writes to the outbox). That's why the user sees fees on the Income Statement when those flow through approvals but nothing else.

The earlier finance recommendations (R2 in `nexusSacco_Finance_Module_Recommendations.md`) fixed the non-approval inline fire-and-forget. **They did not cover the approval-executor case** — that's the residual gap.

### Net effect for the user

In a tenant with default approval toggles on (the production-realistic setup):

- Every member-profile share buy → invisible to Today's receipts AND no GL impact.
- Every member-profile deposit → invisible to Today's receipts AND no GL impact.
- Every member-profile withdrawal → invisible AND no GL impact.
- Every member-profile loan repayment → invisible AND no GL impact.
- Collection Desk fee lines → visible AND posted to GL.
- Collection Desk savings_deposit / share_purchase / loan_repayment lines → visible (the receipt row was created) BUT no GL impact (the executor on approve doesn't post).
- Inline path with toggles off → posts to GL but still no receipt row.

The Collection Desk path getting a receipt row is what fools the user into thinking it works — the page shows the row, but the books are still silently broken when the line is anything other than a fee or welfare contribution.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. There are two systemic bugs in the cash-event pipeline:
>
> 1. Inline panels (Member 360 → Buy shares / Deposit / Withdraw / Repay) write subledger rows but never insert into `receipts` / `receipt_lines`, so they are invisible to "Today's receipts".
> 2. The approval executor in `pending_approvals.executePayloadTx` runs the subledger write (e.g. `ExecuteSharePurchaseTx`) but never calls the matching `postXxxToGLTx` helper, so approved purchases never reach the GL. This affects every money kind except `ApprovalKindFeePosting` / `ApprovalKindWelfarePosting`.
>
> Fix both in one PR. The two fixes share a common refactor (a single place that wraps "subledger write + receipt row + GL post" into one atomic primitive), so doing them together is cheaper than doing them apart.
>
> **Files you will read first**
>
> - `services/savings/internal/db/migrations/0022_collection_desk.up.sql:94–175` — `receipts` + `receipt_lines` schema. Note the `virtual_till_id` column on `receipts` and the CHECK constraint that forces either `till_session_id` (cash) or `virtual_till_id` (non-cash).
> - `services/savings/internal/handler/collection_desk.go::CreateReceipt` — the only path that currently writes a receipt header + lines. Understand the per-line approval queueing loop (≈line 390–455).
> - `services/savings/internal/handler/collection_desk.go::postFeeLineTx` (≈line 818) — the existing reusable GL-outbox writer pattern.
> - `services/savings/internal/handler/share.go::Purchase` (≈line 357), `deposit.go::Deposit` (≈line 372), `deposit.go::Withdraw`, `loan_repayment.go::PostRepayment`. These are the four inline panels.
> - `services/savings/internal/handler/pending_approvals.go::executePayloadTx` (≈line 570–900) — the approval executor that omits GL for every kind except fee/welfare.
> - `services/savings/internal/handler/pending_approvals.go::Approve` (≈line 171) — the wrapper that calls `executePayloadTx` and `propagateToReceiptLine`.
> - `nexusSacco_Finance_Module_Recommendations.md` R2 — covers the original outbox-refactor for the non-approval inline path. This PR is the follow-up that closes the approval-path gap.
>
> **Steps**
>
> ### Step 1: introduce a per-tenant "Inline" virtual till + plumbing
>
> Inline panels (and other non-Collection-Desk paths like the future M-PESA C2B distributor) need a `virtual_till_id` so the receipt schema's CHECK is satisfied for non-cash channels. The `virtual_tills` row that already exists in migration 0022 has a `code` column we can key off.
>
> 1. New migration `services/savings/internal/db/migrations/0038_inline_virtual_till_seed.up.sql`:
>    ```sql
>    BEGIN;
>    -- Seed a per-tenant 'INLINE' virtual till. Drives receipt rows
>    -- written by the inline money panels (Member 360 Buy shares /
>    -- Deposit / Withdraw / Repay) and by future programmatic posters
>    -- (M-PESA C2B distributor, payroll import, etc.) so the
>    -- "Today's receipts" page sees every cash event.
>    INSERT INTO virtual_tills (tenant_id, code, label, gl_suspense_code)
>    SELECT t.id, 'INLINE', 'Inline / programmatic', '1090'
>      FROM tenants t
>     ON CONFLICT (tenant_id, code) DO NOTHING;
>    COMMIT;
>    ```
>    Down: `DELETE FROM virtual_tills WHERE code='INLINE'`.
> 2. Add `services/savings/internal/store/virtual_till_store.go::GetOrCreateInlineTx(ctx, tx, tenantID)` that returns the INLINE virtual_till id, creating it if absent (covers tenants created between migrations).
>
> ### Step 2: extract a `receiptops` package
>
> Both fixes hang off the same primitive: "given a tenant + counterparty + line kind + amount + channel, write a balanced (receipt + receipt_line) pair and return the line id so the caller can stamp `posted_txn_id` later." Today this lives inside `collection_desk.CreateReceipt`. Hoist the reusable part:
>
> 1. New file `services/savings/internal/receiptops/receiptops.go`. Package depends on the existing stores (allowed — same module):
>    ```go
>    package receiptops
>
>    type Deps struct {
>        Receipts     *store.ReceiptStore
>        VirtualTills *store.VirtualTillStore
>        TillSessions *store.TillSessionStore
>        SerialAlloc  *store.SerialAllocator
>    }
>
>    type WriteInput struct {
>        TenantID         uuid.UUID
>        CounterpartyID   uuid.UUID
>        CashierUserID    uuid.UUID
>        Channel          domain.ReceiptChannel
>        ChannelRef       string
>        ChannelAmount    decimal.Decimal
>        ValueDate        time.Time
>        Narration        string
>        Source           string // "inline_share_purchase" | "inline_deposit" | "mpesa_c2b" | …
>        TillSessionID    *uuid.UUID // set when channel == "cash" (caller owns a real till)
>        Lines []LineInput
>    }
>
>    type LineInput struct {
>        Kind            domain.ReceiptLineKind
>        Amount          decimal.Decimal
>        TargetAccountID *uuid.UUID
>        FeeCode         string
>        Narration       string
>        // If a per-line approval is queued, the caller fills this in
>        // after WriteTx returns. Pre-existing approvals (Collection
>        // Desk path) skip the helper entirely.
>        PostedTxnID *uuid.UUID
>    }
>
>    func WriteTx(ctx context.Context, tx pgx.Tx, deps Deps, in WriteInput) (*domain.Receipt, []domain.ReceiptLine, error)
>    ```
>    The function:
>    - When `Channel == "cash"`, requires `TillSessionID != nil`. Otherwise resolves the tenant's INLINE virtual till.
>    - Allocates a `receipts.serial` via the existing serial allocator (mirror what `CreateReceipt` does).
>    - Inserts the `receipts` row and N `receipt_lines` rows, all in `tx`.
>    - Returns the populated structs.
> 2. Refactor `collection_desk.CreateReceipt` to call `receiptops.WriteTx` for the header+lines insert. Behaviour should not change — same inputs, same row contents.
> 3. Tests: `receiptops_test.go` exercising cash + virtual paths, multi-line, idempotency on the unique-channel-ref constraint.
>
> ### Step 3: extract a `postingops` package (mirrors the refactor I wrote up earlier for M-PESA)
>
> Hoist the four GL-outbox writers into a non-`internal` package so both the inline handlers and the approval executor can call them through one seam. This makes step 4 a one-line addition per kind rather than copy-pasted GL plumbing.
>
> 1. New file `services/savings/pkg/postingops/postingops.go`:
>    ```go
>    package postingops
>
>    func PostShareBuyTx(ctx, tx, deps, result, channel) error
>    func PostDepositTx(ctx, tx, deps, result, channel) error
>    func PostWithdrawalTx(ctx, tx, deps, result, channel) error
>    func PostLoanRepaymentTx(ctx, tx, deps, result, channel) error
>    func PostLoanDisbursementTx(ctx, tx, deps, result, channel) error
>    func PostDepositTransferTx(ctx, tx, deps, result) error
>    // … one per current postXxxToGLTx
>    ```
>    Move the bodies of the existing `postSharePurchaseToGLTx`, `postDepositToGLTx`, `postWithdrawalToGLTx`, etc., into this package. The handler-side methods become two-line wrappers that build the `Deps` and call the package function. Tests + behaviour unchanged.
> 2. The accounting service codes (`shareChannelCashAccount`, `depositLiabilityCode`, etc.) come along — they're already pure helpers.
>
> ### Step 4: write a receipt + post to GL from every inline panel
>
> For each of the four inline handlers (`share.Purchase`, `deposit.Deposit`, `deposit.Withdraw`, `loan_repayment.PostRepayment`):
>
> 1. **Inline / toggle-off branch** — already posts to GL via the outbox writer; just add the `receiptops.WriteTx` call alongside it inside the same `WithTenantTx`:
>    ```go
>    res, err := h.ExecuteSharePurchaseTx(r.Context(), tx, payload, userID)
>    if err != nil { return err }
>    // NEW: write the receipt header + line. Single-line receipts here.
>    rec, lines, rerr := receiptops.WriteTx(r.Context(), tx, h.ReceiptDeps, receiptops.WriteInput{
>        TenantID:       tid,
>        CounterpartyID: memberID,
>        CashierUserID:  userID,
>        Channel:        domain.ReceiptChannel(in.PaymentChannel),
>        ChannelRef:     in.PaymentRef,
>        ChannelAmount:  res.Transaction.Amount,
>        Source:         "inline_share_purchase",
>        Lines: []receiptops.LineInput{
>            {Kind: domain.LineSharePurchase, Amount: res.Transaction.Amount, PostedTxnID: &res.Transaction.ID},
>        },
>    })
>    if rerr != nil { return rerr }
>    if perr := postingops.PostShareBuyTx(r.Context(), tx, h.PostingDeps, res, in.PaymentChannel); perr != nil { return perr }
>    ```
>    Then on the line: stamp `posted_txn_id` + flip status to `posted` (or write the line as already-posted by passing `PostedTxnID` to `WriteTx` and letting it insert `status='posted'` directly).
> 2. **Toggle-on branch** — currently queues a `pending_approval` only. Now also writes a *pending* receipt + line (so it shows up in Today's receipts immediately, marked pending) and attaches the approval id to the line:
>    ```go
>    rec, lines, _ := receiptops.WriteTx(... /* status defaults to pending */)
>    pa, _ := h.Approvals.QueueTx(...)
>    h.Receipts.AttachApprovalTx(r.Context(), tx, lines[0].ID, pa.ID)
>    ```
>    On approve, the executor (step 5) flips the line to `posted` and writes the GL outbox row.
> 3. Repeat for deposit, withdrawal, loan repayment.
>
> ### Step 5: make the approval executor write to GL for every kind
>
> In `pending_approvals.executePayloadTx`, after each `Execute…Tx` call that produces a money-moving result, call the matching `postingops.PostXxxTx`:
>
> ```go
> case domain.ApprovalKindDeposit:
>     // … existing payload decode + ExecuteDepositTx …
>     if perr := postingops.PostDepositTx(ctx, tx, h.PostingDeps, res, p.Channel); perr != nil {
>         return nil, nil, perr
>     }
>     txnID := res.Transaction.ID
>     return res, &txnID, nil
>
> case domain.ApprovalKindWithdrawal:
>     // … + postingops.PostWithdrawalTx
>
> case domain.ApprovalKindSharePurchase:
>     // … + postingops.PostShareBuyTx
>
> case domain.ApprovalKindShareTransfer:
>     // … + postingops.PostShareTransferTx     (extract from current share.go handler)
>
> case domain.ApprovalKindLoanDisbursement:
>     // … + postingops.PostLoanDisbursementTx
>
> case domain.ApprovalKindLoanRepayment:
>     // … + postingops.PostLoanRepaymentTx
>
> // … every other kind that today omits the GL post
> ```
>
> The fee/welfare cases (line 806) already do the right thing — leave them alone.
>
> The `propagateToReceiptLine` call after the executor (in `Approve`) continues to flip the receipt line to `posted` — but now there's actually a receipt line to flip for inline-originated approvals, because step 4 created it.
>
> ### Step 6: backfill audit + tests
>
> 1. Each fixed executor branch needs a focused test: seed a counterparty, queue an approval, approve it, assert (a) `journal_entries` row exists with the correct legs, (b) `posting_outbox` row was inserted in the same tx, (c) `receipt_lines.posted_txn_id` is stamped.
> 2. Integration tests: hit each inline panel endpoint with toggles ON, then approve via the approval API, assert end-to-end: receipt visible in `/v1/receipts`, journal entry visible in `/v1/accounting/journal-entries`, balance sheet "Member Share Capital" / "Ordinary Savings Deposits" / "Loans Receivable" moved by the expected amount.
> 3. Same integration tests with toggles OFF — same assertions; everything should still work because step 4 also added the receipt-row write to the inline path.
> 4. Regression: existing Collection Desk fee/welfare path still posts and shows up in receipts (no behaviour change there).
>
> ### Step 7: data backfill for already-processed events
>
> Historical inline-panel transactions are in `share_transactions` / `deposit_transactions` / `loan_payments` with no receipt and no GL row. Decide and write up the backfill policy:
>
> 1. Add a script `services/savings/cmd/inline-receipt-backfill/main.go` that walks `share_transactions` and `deposit_transactions` rows since a cutoff date, finds those without an attached receipt line, and writes the synthetic `receipts` + `receipt_lines` rows attributed to the INLINE virtual till. **Does not write to the GL** — that's a separate decision; existing trial-balance reports would shift if we backfilled the journal entries, and your auditor needs to sign off on that. Document this explicitly in the README header.
> 2. Surface a count to operators: how many transactions per tenant are missing receipts, how many are missing journal entries. This drives the auditor conversation.
>
> ### Acceptance walkthrough
>
> Setup: tenant with default approval toggles ON.
>
> 1. From a member's profile, click "Buy shares" → buy 10 shares. The page shows "Pending approval". Open `/collect/receipts` — the receipt row is there with status `pending`. Open the Approvals inbox — the share purchase is there.
> 2. Approve the share purchase as a checker. Re-open `/collect/receipts` — the row is now `posted`. Open `/accounting/journal-entries` — there's a balanced JE: DR cash / CR Member Share Capital. Balance Sheet "Member Share Capital" went up by the par-value × 10. SASRA return reflects the change.
> 3. Repeat for a deposit (toggle ON, then OFF), withdrawal, loan repayment. Each lands on Today's receipts; each posts a JE; each moves the matching Balance Sheet line.
> 4. Toggle approvals OFF and repeat the inline share-buy. Same outcome — receipt + JE both land immediately. (Step 4 added the receipt write to the inline path.)
> 5. From the Collection Desk, post a receipt with one savings_deposit line and one fee line, toggle ON. Approve. Both lines land on the GL (the savings_deposit line is the regression fix; the fee was already working). The receipt row was already visible — verify it still is.
> 6. Run the inline-receipt-backfill script in dry-run mode against the dev DB. It reports "N share_transactions missing receipts, M deposit_transactions missing receipts, J unallocated journal entries". Run with `--apply` and re-check; missing-receipts count drops to zero.
>
> ### Idempotency / safety
>
> - The `receipts.channel_ref_unique` constraint means the inline paths must pass a deterministic `ChannelRef` to avoid spurious dupe-rejections — use `f.txn_id.String()` for cash (channel_ref is NULL for cash already; the constraint uses NULLS NOT DISTINCT, so multiple cash receipts per till per day are fine) and `payment_ref` for non-cash. Document this in the receiptops helper.
> - The approval-executor GL post must use the same `(source_module, source_ref)` shape today's inline path uses (`source_ref = transaction.id.String()`). Accounting's dedup on that pair makes the executor idempotent if a checker double-clicks Approve.
> - Migration 0038 must be a no-op when re-run (`ON CONFLICT DO NOTHING`).
> - `gofmt`, `go vet`, `go test ./services/savings/... ./services/accounting/...` all green before opening the PR.
> - The backfill script lives behind a `--apply` flag; default is dry-run.
>
> When done, paste the diff stat + the per-kind audit table (which `ApprovalKind` now posts to GL vs which did before) + the acceptance screenshots into the PR description.

---

## Why this shape

The two bugs share a single failure mode: there is no single seam through which "a cash event happened" is recorded. Today it's split between the Collection Desk's receipt-table writes and the inline handlers' subledger writes, with the approval executor sitting in the middle and only sometimes posting to GL. Hoisting `receiptops` and `postingops` into reusable packages and threading them through both the inline handlers AND the approval executor means there is now exactly one way for a cash event to enter the books: receipt row + GL outbox row + subledger row, all in the same tx, regardless of which UI button triggered it.

The seam also pays for itself the next time another caller needs to record a money event — the M-PESA C2B distributor (phase 3 of the M-PESA work) imports `receiptops` and `postingops` directly. Same with the future payroll-import worker, the dividend-payout dispatcher, and any other programmatic poster you might add.

The backfill script is offered separately because writing journal entries for historical transactions is an auditor-policy decision, not an engineering one. The script gives you the numbers; the call on whether to apply the GL backfill belongs to the SACCO's accountant.