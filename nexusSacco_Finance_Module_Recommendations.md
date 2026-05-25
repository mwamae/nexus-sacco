# nexusSacco — Finance Module End-to-End Review

**Date:** 25 May 2026
**Reviewer:** Finance + software engineering pass
**Scope:** `services/accounting`, posting hooks in `services/savings` and `services/member`, finance reports (Income Statement, Balance Sheet, SASRA Return, Cash Flow, Dashboard), and the chart-of-accounts ↔ events plumbing.

---

## 1 · Why the reports stay flat

Every finance report in the system reads exclusively from posted journal entries:

| Report | Source query |
|---|---|
| Income Statement (`/v1/reports/income-statement`) | `journal_entries` × `journal_lines` × `chart_of_accounts` where `class IN ('income','expense')` |
| Balance Sheet (`/v1/reports/balance-sheet`) | same join, classes asset/liability/equity |
| SASRA Return (`/v1/reports/sasra-return`) | same join + bucketing by `chart_of_accounts.type` |
| Cash Flow (`/v1/reports/cash-flow`) | account-level deltas from the GL |
| Dashboard (`/v1/reports/dashboard`) | KPIs + 12-month trend from the GL |

That is the right architecture. The reports are working correctly. What is broken is **the upstream plumbing that is supposed to feed the GL**. Several money-movement paths either post the wrong account, post outside the business transaction (and silently swallow failures), or do not post at all.

Below are the gaps, the file & line where each is rooted, and a self-contained Claude Code prompt for each.

---

## 2 · Gap map

| # | Event / Path | Symptom | Root cause |
|---|---|---|---|
| **R1** | Fee Collection Desk (`fee` + `welfare` lines) | Membership / statement / closure / ad-hoc fees never reach the Income Statement; welfare appears as a liability not income | `services/savings/internal/db/migrations/0023_fee_catalog.up.sql` seeds GL codes (`4110`/`4120`/`4130`/`4190`/`2510`) that don't exist in the chart of accounts or are mis-classed (`2510` is "Institutional Loans") |
| **R2** | Deposits / Withdrawals / Share Purchase / Loan Disbursement / Loan Repayment | GL can be silently out-of-sync with the subledger after a single network blip | `postDepositToGL` / `postWithdrawalToGL` / `postSharePurchaseToGL` / `postLoanDisbursementToGL` / `postRepaymentToGL` run **after** `WithTenantTx` commits — failures are logged, not rolled back |
| **R3** | Interest Run posting | Interest expense, the WHT payable, and the savings credit never appear on the Income Statement or Balance Sheet | `services/savings/internal/handler/interest.go::Post()` and `postLine` write `deposit_transactions` + `tax_payable_ledger` but make zero calls to `posting.Client` |
| **R4** | Dividend Run posting | Declared dividends never debit "Dividend Expense", never credit member savings, never move retained earnings; SASRA institutional-capital line is wrong | `services/savings/internal/handler/dividend.go::Post()` and `postDivLine` — same pattern as interest, no `posting.Post` call |
| **R5** | Share Transfer / Adjust / Bonus Issue / Lien | Share-side movements other than Purchase don't touch the GL — equity can drift from the subledger | `share.go::Transfer`, `Adjust`, `PlaceLien`, `ReleaseLien`, `BonusIssue` have no `postXxxToGL` helper |
| **R6** | Tenant fiscal year ≠ calendar year | SASRA "income statement" section reports wrong window for tenants whose FY starts in (e.g.) July | `report_store.go::SASRAReturnTx` hard-codes `fyStart := time.Date(asOf.Year(), 1, 1, ...)`, ignoring `tenant_operations.fy_start_month/day` |
| **R7** | No fees & collections summary | Even when fees post correctly, there's no operational view tying receipts → fee codes → GL revenue | No `/v1/reports/fees-summary` endpoint; fee catalog has no usage rollup |
| **R8** | No subledger ↔ GL reconciliation | Drift is silent — admin only finds out at year-end audit | No report joining `deposit_accounts`, `loans`, `share_accounts` totals to GL accounts `2000-2050` / `1100` / `3000` |
| **R9** | System-initiated postings are unauditable | Batch postings (interest, dividends, depreciation, provisioning, year-end close) have no append-only log of who/when/why-fired or why-skipped | `services/savings` and `services/accounting` log to slog but don't write a structured `system_postings_log` row keyed by run id |
| **R10** | No CI guard against new "skip-the-GL" handlers | Future cash handlers will continue to drift | No analyzer / `rg` rule enforcing that every money-moving handler returns through `posting.Post` and is inside `WithTenantTx` |

---

## 3 · Recommendations and Claude Code prompts

Each prompt below is self-contained — copy and paste into a fresh Claude Code session. Prompts are sized so each one delivers a coherent PR; **ship them in the listed order** because R1 and R2 are prerequisites for the others to be observable.

---

### R1 — Fix the fee catalog GL mapping so fees actually surface as income

**Claude Code prompt:**

> You are working on `nexusSacco`. The fee catalog seeded by `services/savings/internal/db/migrations/0023_fee_catalog.up.sql` maps fee codes to GL credit accounts that **do not exist** in the chart of accounts (CoA seeded by `services/accounting/internal/db/migrations/0001_init.up.sql`):
>
> - `membership_registration` → `4110` (doesn't exist)
> - `statement_fee` → `4120` (doesn't exist)
> - `account_closure` → `4130` (doesn't exist)
> - `ad_hoc` → `4190` (doesn't exist)
> - `welfare_contribution` → `2510` (exists but is **Institutional Loans**, a long-term liability — wildly wrong)
>
> Consequence: when a cashier collects a fee, the Collection Desk's `postFeeLineTx` calls `posting.Post` which fails with `ErrUnknownAccount` (rolling back the receipt), or for `welfare_contribution` posts to Institutional Loans and inflates external borrowings on the SASRA return.
>
> Do all of the following in **one PR**:
>
> 1. **Add missing income accounts to the CoA.** Create `services/accounting/internal/db/migrations/0012_fee_income_accounts.up.sql` that backfills these system-locked accounts into every existing tenant (mirroring the pattern in migration `0008`):
>     - `4110` — "Membership Registration Fee Income" (income / fee_income / credit)
>     - `4120` — "Statement Fee Income" (income / fee_income / credit)
>     - `4130` — "Account Closure Fee Income" (income / fee_income / credit)
>     - `4190` — "Other Ad-Hoc Fee Income" (income / fee_income / credit)
>     - `2300` — "Welfare Fund Payable" (liability / current_liability / credit) — welfare contributions are member funds held in trust for welfare scheme payouts, **not** SACCO income
>     - Include the matching `.down.sql`.
>
> 2. **Reconcile the fee catalog mapping.** In `services/savings/internal/db/migrations/0024_fee_catalog_gl_reconcile.up.sql`:
>     - `UPDATE fee_catalog SET gl_credit_code = '2300' WHERE code = 'welfare_contribution'` (re-route welfare from `2510` Institutional Loans to the new Welfare Fund Payable liability).
>     - `UPDATE fee_catalog SET gl_credit_code = '4080' WHERE code = 'membership_registration'` (consolidate with the existing Registration Fee Income code that the application_fee_payments path already uses — having two codes for the same revenue stream is what created the gap in the first place).
>     - Leave `4110`, `4120`, `4130`, `4190` in place since they now exist in the CoA.
>     - Backfill is idempotent (no-op on tenants that already changed the mapping by hand).
>
> 3. **Lock the codes the cashier UI can pick.** In `services/savings/internal/store/fee_catalog_store.go::CreateTx`, validate that the supplied `gl_credit_code` resolves against the CoA on the same tenant before the row is inserted. Return a typed `ErrUnknownGLCode` and surface it as a 422 with a human-readable error in the admin "Manage fees" page. Add a unit test that asserts the error.
>
> 4. **Backfill any historical posting failures.** Add a one-off, non-destructive admin endpoint `POST /v1/fees/replay-failed` (gated on the `finance_admin` role) that finds receipt lines where `kind IN ('fee','welfare')` and `posted_txn_id IS NULL` AND `voided_at IS NULL`, re-runs `postFeeLineTx` for each, and returns a summary `{replayed, skipped, still_failed}`. Lines that still fail emit a `payload` field with the underlying engine error so the admin can spot any remaining gaps.
>
> 5. **Acceptance walkthrough** (include in PR description):
>     - Run all three migrations on the demo tenant.
>     - Collect a membership_registration fee of `1,000.00` via the Collection Desk → confirm receipt posts, `4080` credited, Income Statement for current month shows `+1,000.00 Registration Fee Income`.
>     - Collect a welfare_contribution of `200.00` → confirm `2300` Welfare Fund Payable credited (Balance Sheet — Liability), no income effect.
>     - SASRA "Borrowings" ratio is unchanged by welfare collections (regression test for the `2510` mis-route).
>     - Trying to create a fee catalog entry with a non-existent GL code returns 422 with the message `unknown GL account "9999"`.

---

### R2 — Bring GL posting **inside** the business transaction (atomic)

**Claude Code prompt:**

> You are working on `nexusSacco`. The deposit, withdrawal, share-purchase, loan-disbursement, and loan-repayment handlers in `services/savings/internal/handler/` post to the GL **after** their `WithTenantTx` block has returned, which means:
>
> - If the accounting service is briefly unreachable, the deposit row is committed and the GL is permanently missing the entry — only an error line in the slog log shows it.
> - The "no transaction is financially complete without a GL entry" invariant documented in `services/savings/internal/posting/client.go` is broken by the very handlers that file is meant to serve.
>
> Files showing the pattern:
> - `services/savings/internal/handler/deposit.go:458` (`postDepositToGL` after `WithTenantTx` returns)
> - `services/savings/internal/handler/deposit.go:559` (`postWithdrawalToGL`)
> - `services/savings/internal/handler/share.go:440` (`postSharePurchaseToGL`)
> - `services/savings/internal/handler/loan.go::Disburse` calling `postLoanDisbursementToGL` after commit
> - `services/savings/internal/handler/loan_repayment.go:156` (`postRepaymentToGL` after commit)
>
> Refactor each so the GL post **lives inside** the same `WithTenantTx`:
>
> 1. Add `postDepositToGLTx(ctx, tx, ...) error` variants that call `h.Posting.PostTx(ctx, tx, ...)` — a new method on `services/savings/internal/posting/client.go` that, when the client has a non-nil `Engine *postingengine.Engine` injected (in-process composition mode), posts via the in-process posting engine using the **same tx**. When `Engine` is nil (HTTP mode), it falls back to the existing HTTP `Post` — but the HTTP call is made *after* commit on a deferred goroutine guarded by an outbox row (see step 3) so service-to-service calls don't deadlock the request tx.
>
> 2. Treat any in-tx posting failure as a hard error — propagate it out of `WithTenantTx` so the deposit, withdrawal, share, or loan write rolls back. Surface it to the caller as `502 Bad Gateway` with `{ "code": "gl_post_failed", "detail": "<engine error>" }`.
>
> 3. **For HTTP-mode tenants** (microservices deployment), insert into a new `services/savings/internal/db/migrations/0040_posting_outbox.up.sql`:
>     ```sql
>     CREATE TABLE posting_outbox (
>       id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
>       tenant_id     uuid NOT NULL,
>       payload       jsonb NOT NULL,         -- the PostInput body
>       attempts      int NOT NULL DEFAULT 0,
>       last_error    text,
>       enqueued_at   timestamptz NOT NULL DEFAULT now(),
>       dispatched_at timestamptz,
>       posted_je_id  uuid                    -- stamped on success
>     );
>     CREATE INDEX posting_outbox_pending_idx
>       ON posting_outbox(tenant_id, dispatched_at, enqueued_at);
>     ```
>     The handler inserts the outbox row inside the same tx as the business write. A background worker (`cmd/posting-dispatcher`) reads pending rows, calls the accounting service, stamps `dispatched_at` + `posted_je_id` on success or bumps `attempts` + `last_error` on failure (exponential backoff, hard-fail after 12 attempts → on-call alert).
>
> 4. Add `/v1/finance/posting-outbox?status=stuck` for operators to see rows where `attempts >= 3 AND dispatched_at IS NULL`. Provide a `POST .../{id}/replay` action.
>
> 5. **Acceptance walkthrough:**
>     - Kill the accounting service. Issue a deposit. Confirm: deposit returns `201`, GL entry is in the outbox table, NOT in `journal_entries`. Start the dispatcher → after 1s the entry posts and the outbox row is stamped. (HTTP mode.)
>     - With in-process posting engine wired: kill the GL post path artificially in `postDepositToGLTx`. Confirm the deposit returns 502 AND the `deposit_transactions` row is absent.
>     - Existing tests must still pass — no behaviour change when posting succeeds.

---

### R3 — Wire the Interest Run to the General Ledger

**Claude Code prompt:**

> You are working on `nexusSacco`. The Interest Run posting endpoint at `POST /v1/interest-runs/{id}/post` (`services/savings/internal/handler/interest.go::Post`, lines 528-586) computes interest per member, writes `deposit_transactions` and `tax_payable_ledger` rows, but **never posts to the General Ledger**. As a result:
>
> - "Interest on Member Deposits" (CoA `5000`) stays at zero on the Income Statement no matter how many interest runs complete.
> - "Withholding Tax Payable" (CoA `2200`) stays at zero on the Balance Sheet.
> - The member savings credit appears on `deposit_transactions` but not in `journal_lines`, so the trial balance silently drifts.
>
> Wire the Interest Run to the GL. In `services/savings/internal/handler/interest.go::Post`:
>
> 1. Inject `Posting *posting.Client` into `InterestHandler` (mirror the pattern in `DepositHandler`). Update `services/savings/cmd/server/main.go` to supply it.
>
> 2. After the per-line loop completes and lines are marked posted, build a **single batched journal entry** that aggregates the run:
>     - DR `5000` Interest on Member Deposits = sum(gross_interest) on all `posted` lines
>     - CR per-product savings liability (resolve via the same `resolveLiabilityAcct` helper used in `deposit.go` — group by product, one credit line per product) = sum(net_interest credited to savings) per product
>     - CR `2200` Withholding Tax Payable = sum(wht_amount) on all lines
>     - For lines with `PayoutMethod = buy_shares`: instead of crediting savings, CR `3000` Member Share Capital (a "stock dividend" style movement).
>     - For lines with `PayoutMethod = external`: credit `2230` Other Payables (the SACCO owes the member but it hasn't moved yet).
>
> 3. Source-ref the entry with `source_module = "savings.interest"`, `source_ref = run.ID.String()`. The accounting service idempotency key on `(source_module, source_ref)` means re-running `/post` after a partial failure is safe.
>
> 4. The posting **must happen inside** the same `WithTenantTx` that flips the run to `posted`. A failure rolls back the run state change so the operator can retry.
>
> 5. Add an integration test (using the in-memory accounting engine pattern from `services/savings/internal/handler/collection_desk_acceptance_test.go`) that runs a 3-member interest declaration, posts it, and asserts:
>     - One `journal_entries` row exists with `source_module = 'savings.interest'` and `total_debits = total_credits = sum(gross)`.
>     - Income Statement for the period shows `5000` with the expected balance.
>     - SASRA Return's `IncomeStatement.TotalExpense` now includes the interest expense.
>
> 6. Schema check: ensure `interest_runs` has a `journal_entry_id uuid REFERENCES journal_entries(id)` column. If absent, add a migration `services/savings/internal/db/migrations/004X_interest_run_je_link.up.sql` that adds it nullable; populate it inside the posting flow.

---

### R4 — Wire the Dividend Run to the General Ledger

**Claude Code prompt:**

> You are working on `nexusSacco`. The Dividend Run posting endpoint at `POST /v1/dividend-runs/{id}/post` (`services/savings/internal/handler/dividend.go::Post`, lines 436-486) has the same gap as the Interest Run: it credits members in `deposit_transactions` (or `share_transactions` for buy-shares) and writes `tax_payable_ledger`, but **never posts to the GL**.
>
> Effect: declared dividends never reduce retained earnings, never show as Dividend Expense, never move share capital when the payout mode is `buy_shares`. The SASRA `institutional_capital` line and the changes-in-equity report both lie.
>
> Wire dividends to the GL with the same shape as R3. Specifically:
>
> 1. Inject `Posting *posting.Client` into `DividendHandler`. Wire it from `services/savings/cmd/server/main.go`.
>
> 2. After the per-line loop completes, build a single appropriation journal entry:
>     - DR `3010` Retained Earnings = sum(gross_dividend across all lines) — this is an **equity transfer**, not a P&L expense. The SACCO accounting spec treats dividend declaration as an appropriation of retained earnings, not an operating expense.
>     - CR per-product savings liability (resolve via product like in R3) for cash-credit lines.
>     - CR `3000` Member Share Capital for buy-shares lines.
>     - CR `2230` Other Payables for external-payout lines.
>     - CR `2200` Withholding Tax Payable = sum(wht_amount).
>
> 3. (If the existing `services/accounting/internal/db/migrations/0007_dividend_appropriation.up.sql` already has a posting helper that books this entry, **use it** rather than building a parallel implementation — track down the helper and call it from the dividend handler so the appropriation logic stays in one place. If you find duplicated logic, delete the dead path and leave a comment pointing to the canonical location.)
>
> 4. Same idempotency rules as R3: `source_module = "savings.dividend"`, `source_ref = run.ID.String()`. Same in-tx execution.
>
> 5. Add an integration test asserting:
>     - A 100,000 KES dividend declared across 50 members debits retained earnings by 100,000.
>     - Statement of Changes in Equity for the period shows a `-100,000` decrease line under Retained Earnings.
>     - SASRA `Capital.RetainedEarnings` and `Capital.CoreCapital` both drop by 100,000 (less the unclosed surplus offset).
>     - Re-posting the same run is a no-op (idempotency check).

---

### R5 — Wire share Transfer / Adjust / Bonus / Lien to the GL

**Claude Code prompt:**

> You are working on `nexusSacco`. `services/savings/internal/handler/share.go` posts to the GL only for `Purchase` (line 440). The other share-affecting endpoints — `Transfer` (line 506), `Adjust` (716), `PlaceLien` (814), `ReleaseLien` (887), and `BonusIssue` (933) — write `share_transactions` rows but never produce a journal entry. Effect: Member Share Capital (CoA `3000`) on the Balance Sheet diverges from the subledger after the first transfer or bonus issue.
>
> For each path, add a `postXxxToGLTx` (inside the existing `WithTenantTx`, per R2) and wire it after the share-side write succeeds:
>
> | Endpoint | Debit | Credit | Source module |
> |---|---|---|---|
> | `Transfer` | (none — same account, no money movement) | (none — equity transfer between members is a subledger note only) | n/a — **but** add a single audit entry `share_transfers_audit` with both member IDs + JE id NULL, so the operations team can reconcile share registry against the GL by member without expecting GL entries |
> | `Adjust` (admin correction) | varies | varies | `savings.shares.adjust` — operator must provide a justification + offsetting account when adjusting share count, defaults: increase → DR `3010` / CR `3000`; decrease → DR `3000` / CR `3010` |
> | `PlaceLien` | (none — no GL movement, the shares are encumbered not transferred) | (none) | Add a `share_liens_audit` row only; on the share GL detail view show a "Liened" badge |
> | `ReleaseLien` | (none) | (none) | Audit only |
> | `BonusIssue` | DR `3010` Retained Earnings | CR `3000` Member Share Capital | `savings.shares.bonus` — this is a stock dividend, exactly the same as the buy-shares branch of R4 but issued without first declaring cash |
>
> For `Adjust`, require an `adjustment_reason` and an offsetting account code in the request body. Reject the call if the offsetting account doesn't exist or has the wrong class (must be equity or expense). Surface the constraint in the admin UI.
>
> Acceptance walkthrough: issue a 50,000 KES bonus to existing shareholders → assert RE drops by 50,000 and Share Capital rises by 50,000 in the GL and on the Statement of Changes in Equity.

---

### R6 — Make SASRA Return honour the tenant's fiscal year

**Claude Code prompt:**

> You are working on `nexusSacco`. The SASRA return endpoint at `/v1/reports/sasra-return` hard-codes the income-statement window to a calendar year:
>
> ```go
> fyStart := time.Date(asOf.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
> ```
>
> at `services/accounting/internal/store/report_store.go:919` (inside `SASRAReturnTx`). The same hard-code repeats inside `DashboardTx` at line 1258. But the tenant's actual fiscal year is configured in `tenant_operations.fy_start_month` / `fy_start_day` (used by the interest engine and the year-end close workflow). Mismatch → SASRA reports the wrong income window for any SACCO not on January-start.
>
> Fix:
>
> 1. Add a `func (s *ReportStore) fiscalYearStartTx(ctx context.Context, tx pgx.Tx, asOf time.Time) (time.Time, error)` helper that reads `fy_start_month`, `fy_start_day` from `tenant_operations`, defaults to (1, 1), and returns the **most recent** FY start at or before `asOf` (so an as-of of `2026-05-25` on a July-start tenant returns `2025-07-01`).
>
> 2. Replace the two hard-coded `fyStart` lines (in `SASRAReturnTx` and `DashboardTx`) with the helper.
>
> 3. Add a `fiscal_year_start` field to the SASRA Return DTO so the UI can display "Income statement: 01 Jul 2025 → 25 May 2026" rather than implying a calendar year.
>
> 4. Add a unit test using the `sasra_return_bosa_test.go` harness that seeds `tenant_operations(fy_start_month=7, fy_start_day=1)` and asserts the SASRA `IncomeStatement.FromDate` is the prior 1 July, not 1 January.

---

### R7 — Fees & Collections summary report

**Claude Code prompt:**

> You are working on `nexusSacco`. Cashiers post fees via the Collection Desk daily, but the finance team has no operational view of "what fees were collected, by code, by channel, in this window — and what GL accounts they hit". Today they would have to dig the GL detail page per fee-income account, which loses the fee_code dimension entirely.
>
> Build a **Fees & Collections Summary** report:
>
> 1. Backend — `GET /v1/reports/fees-summary?from=YYYY-MM-DD&to=YYYY-MM-DD&channel=&fee_code=&group_by=` in `services/savings/internal/handler/`. The query joins `receipts × receipt_lines × fee_catalog`:
>     - For each fee code: count, total amount, sum of voided amount, net amount, the GL credit account it lands on, and the matching `journal_entries.id` for traceability.
>     - For each channel (cash / mpesa / bank / etc.): same breakdown.
>     - For lines where `posted_txn_id` IS NULL after 5 minutes: list them as **unposted** (this surfaces drift caused by accounting-service outages — see R2's outbox).
>     - Returns `{from, to, total_amount, total_voided, net_amount, by_fee_code: [...], by_channel: [...], unposted: [...]}`.
>
> 2. Frontend — new page `web/admin/src/pages/Accounting/FeesSummary.tsx` (route `/accounting/fees-summary`, link it from the Reports section of `AppShell.tsx`):
>     - Filter bar: From / To / Channel / Fee code.
>     - Three blocks: Totals card · "By fee code" table (with drill-in to GL detail) · "By channel" table.
>     - "Unposted" table only shown when non-empty, with a CTA `Replay posting` calling the endpoint from R1.5.
>     - XLSX export via the existing `downloadReport` helper.
>
> 3. The same endpoint must also be wired into the **Member Profile → Transactions** filter so a user can ask "show me all fees this member has paid" without leaving the page.
>
> 4. Acceptance: after a day of 20 fee receipts across 3 codes + 4 channels, the report sums to exactly the same total as the `4080 + 4110 + 4120 + 4130 + 4190` accounts on the Income Statement for that window (excluding void days).

---

### R8 — Subledger ↔ GL reconciliation report

**Claude Code prompt:**

> You are working on `nexusSacco`. There is no automated way to detect when the GL drifts from the subledger. If a deposit's GL post fails (see R2) or an interest run skips the GL entirely (R3), the subledger says 500,000 KES of member savings, the GL says 480,000 — and nobody knows until year-end audit.
>
> Build a **Subledger Reconciliation** report:
>
> 1. Backend — `GET /v1/reports/reconciliation?as_of=YYYY-MM-DD` in `services/accounting/internal/handler/reports.go`. Compares per-account:
>
>     | Account | GL source | Subledger source |
>     |---|---|---|
>     | `2000`/`2010`/`2020`/`2030`/`2040` (FOSA) | sum of `journal_lines` for credit balance | sum of `deposit_accounts.current_balance` joined to `deposit_products.gl_liability_code` |
>     | `2050`/`2052`/`2053`/...(BOSA) | same | same, filtered by product segment = BOSA |
>     | `2100` Fixed Deposits | same | sum of `fixed_deposits.principal_balance + accrued_interest` (or equivalent) |
>     | `1100` Member Loans Receivable | debit balance | sum of `loans.principal_balance` where status IN ('active', 'in_arrears', 'restructured') |
>     | `1110` Loan Interest Receivable | debit balance | sum of `loans.interest_balance` |
>     | `1120` Loan Loss Provision | credit balance | sum of `loans.provision_balance` |
>     | `3000` Member Share Capital | credit balance | sum of `share_accounts.shares_held * par_value_at_issue` |
>     | `2200` WHT Payable | credit balance | sum of `tax_payable_ledger.wht_amount` where `remitted_at IS NULL` |
>
>     For each row: GL balance, subledger balance, delta, delta as % of GL, and a `status` of `ok` (delta < 1.00 KES), `warn` (delta < 0.1% of GL), or `error` (anything else). Include the SQL identifier of the most recent JE on that account so investigation has a starting point.
>
> 2. Frontend — `web/admin/src/pages/Accounting/Reconciliation.tsx`, surfaced under Finance → Reports → Reconciliation:
>     - Big traffic-light status at the top.
>     - One row per account with the four columns.
>     - "Investigate" CTA on a non-OK row deep-links to the GL detail page for the account + a side-panel listing recent unposted entries from the outbox.
>
> 3. Scheduled-task hook: register a daily scheduled task (`mcp__scheduled-tasks` is in this stack) that hits the reconciliation endpoint and sends an email to the `finance_admin` role if `status != 'ok'` on any row.
>
> 4. Acceptance: artificially break a deposit by deleting the JE row. Run the report. Confirm: row for `2000` flips to status=error with the correct delta. Re-create the JE via manual entry. Re-run. Status flips back to ok.

---

### R9 — Audit log for system-initiated postings

**Claude Code prompt:**

> You are working on `nexusSacco`. Batch / scheduled postings — interest declarations, dividend runs, depreciation, provisioning, fee collection settlement, year-end close — fire from runtime code paths (`services/savings`, `services/accounting`) but only emit slog lines on success or failure. There is no append-only structured trail showing *which* batch fired, *how much* it moved, *who* approved it, *what* journal entries it produced, or *why* it skipped a row.
>
> Build a `system_postings_log` table + write-through:
>
> 1. Migration `services/accounting/internal/db/migrations/0013_system_postings_log.up.sql`:
>     ```sql
>     CREATE TABLE system_postings_log (
>       id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
>       tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
>       run_kind      text NOT NULL,          -- 'interest_run' | 'dividend_run' | 'depreciation' | 'provisioning' | 'fee_settlement' | 'fy_close'
>       run_id        uuid NOT NULL,          -- the upstream run id
>       journal_entry_id uuid REFERENCES journal_entries(id),
>       total_debits  numeric(20,2) NOT NULL,
>       total_credits numeric(20,2) NOT NULL,
>       line_count    int NOT NULL,
>       skipped_count int NOT NULL DEFAULT 0,
>       skipped_payload jsonb,                -- list of {entity_id, reason}
>       triggered_by  uuid,                   -- user_id or NULL for cron
>       triggered_at  timestamptz NOT NULL DEFAULT now(),
>       outcome       text NOT NULL CHECK (outcome IN ('posted','failed','partial')),
>       error_detail  text,
>       UNIQUE (tenant_id, run_kind, run_id)
>     );
>     -- Enforce append-only.
>     CREATE OR REPLACE RULE no_update_system_postings_log AS ON UPDATE TO system_postings_log DO INSTEAD NOTHING;
>     CREATE OR REPLACE RULE no_delete_system_postings_log AS ON DELETE TO system_postings_log DO INSTEAD NOTHING;
>     ```
>
> 2. Insert one row at the end of each batch poster:
>     - Interest run (`services/savings/internal/handler/interest.go::Post`, the new path from R3)
>     - Dividend run (R4)
>     - Provisioning (`services/savings/internal/handler/provisioning.go::Post`)
>     - Depreciation (`services/accounting/internal/handler/fixed_assets.go::PostRun`)
>     - Year-end close (`services/accounting/internal/handler/fiscal_year.go`)
>     - Fee-settlement when R7's replay endpoint is run.
>
> 3. Endpoint `GET /v1/finance/system-postings?run_kind=&from=&to=` + page `web/admin/src/pages/Accounting/SystemPostings.tsx` rendering the log as a table with filters + a drill-in to the JE.
>
> 4. Scheduled-task: a weekly "Finance audit digest" job that emails a Markdown summary of the past 7 days' batch postings (counts, totals, anomalies) to the `finance_admin` role.
>
> 5. Acceptance: run an interest declaration → assert one `system_postings_log` row exists with `outcome='posted'`, `journal_entry_id` populated, `total_debits = total_credits = sum(gross)`. Attempt UPDATE / DELETE → no effect (append-only enforced).

---

### R10 — CI guard so future cash handlers cannot skip the GL

**Claude Code prompt:**

> You are working on `nexusSacco`. After fixing the gaps in R1-R5, we need a guard that catches future regressions where a developer adds a money-moving handler but forgets to wire it to the posting engine, **or** adds a fee catalog entry pointing at a non-existent GL code, **or** moves a GL post outside of `WithTenantTx`.
>
> Add the following safeguards:
>
> 1. **Go static analyzer** at `tools/cmd/postingcheck/main.go` (built on `golang.org/x/tools/go/analysis`). The analyzer runs in CI (`make lint` and the existing GitHub Actions job) and flags:
>     - Any function in `services/*/internal/handler/*.go` that calls `h.Deposits.PostTxnTx`, `h.Shares.PostTxnTx`, `h.Loans.PostTxnTx`, `h.LoanRepayments.PostTxnTx`, or that mutates `deposit_transactions` / `share_transactions` / `loan_transactions` directly via tx.Exec, **without** also calling either `h.Posting.PostTx` (in-tx) or inserting into `posting_outbox`.
>     - Any call to `Posting.Post(` (the HTTP path) that is outside `WithTenantTx` and not preceded by an `outbox.Enqueue` call. (Heuristic; flag for human review rather than hard fail.)
>
> 2. **Schema check at startup** in `services/savings/cmd/server/main.go`: on boot, validate `SELECT gl_credit_code FROM fee_catalog WHERE gl_credit_code NOT IN (SELECT code FROM chart_of_accounts WHERE tenant_id = fee_catalog.tenant_id)`. If the query returns rows, log an `ERROR` line and surface the list on a startup health endpoint `/healthz/finance` so the deployment can fail loudly rather than ship a broken catalog.
>
> 3. **Acceptance walkthrough:**
>     - In a fresh branch, add a `func (h *FakeHandler) BadDeposit()` that posts a deposit transaction without calling `Posting.PostTx`. Run `make lint`. Confirm the analyzer flags it with the file:line + a remediation message.
>     - Manually `UPDATE fee_catalog SET gl_credit_code = '9999' WHERE code = 'ad_hoc'`. Restart `savings` service. Confirm `/healthz/finance` returns 503 and the log line names the offending code.
>     - All existing handlers pass the analyzer (run on `main` once after the rest of the work above lands).

---

## 4 · Sequencing notes

- **Ship R1 first** — the fee codes don't exist, so nothing else surfaces correctly.
- **R2 next** — atomicity. Without it, fixing R3 / R4 / R5 only narrows the surface but doesn't close it.
- **R3, R4, R5 in parallel** once R2 lands — they're independent posters.
- **R6** small win, ship anytime — but useful before R8 so the reconciliation window honours fiscal year.
- **R7 & R8** are the operator-visibility layer; ship after R1-R5 so the reports they expose are honest.
- **R9 & R10** are the safety net — ship last, but ship them. Without R10 the next cash handler will silently break the wiring again.

---

## 5 · What this delivers (in business terms)

After the ten PRs land:

- Every fee, deposit, withdrawal, share movement, loan disbursement, loan repayment, interest declaration, dividend run, provisioning run, and depreciation run produces **exactly one balanced journal entry** with idempotency on `(source_module, source_ref)`.
- The Income Statement reflects fees and welfare and interest expense the moment they're collected or declared.
- The SASRA Return uses the tenant's fiscal year and includes all income / expense, not a calendar-year subset.
- Operators can answer "what fees did we collect last week, by code and channel?" without opening the GL.
- Drift between subledgers and the GL is detected daily, not annually.
- System-initiated postings are append-only-logged and the finance team gets a weekly audit digest.
- A CI guard prevents the next regression.

Each prompt is sized to fit one Claude Code PR. Hand them off in the listed order.
