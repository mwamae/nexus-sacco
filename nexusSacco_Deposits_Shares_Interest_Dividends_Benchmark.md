# Deposits, Shares, Interest & Dividends — Benchmark, Wiring Check & Recommendations

**For Mike. No prompts yet. Review this, request changes if needed, then green-light implementation.**

---

## 1. Executive summary

The four modules (Deposits, Shares, Interest, Dividends) are **architecturally well-modelled** but have **specific operational and feature gaps** compared with established Kenyan SACCOs (Stima, Mwalimu, Tower, Unaitas, Mhasibu, Kenya National Police, Sheria, Centenary, Bandari, etc.).

The good news: every module wires correctly into the General Ledger via the consolidated outbox pattern from the earlier finance fix work. BOSA/FOSA segmentation is in place. The interest engine is sophisticated (AGM-rate, weighted-average, WHT, three payout methods, full lifecycle). Dividend runs follow the same shape.

The gaps fall into three categories:

- **Operational must-fix** — broken or incomplete features that block real-world SACCO use (standing orders, joint accounts, minor/junior accounts, share certificates, member statements, WHT remittance tracking).
- **Wiring verification needs** — the modules touch each other and the loans + finance modules in ways that should be confirmed end-to-end after recent consolidation work (multiplier basis correctness, BOSA lien / collateral linkage, dividend offset wiring per Loans Phase 4).
- **Nice-to-have polish** — standard SACCO features that improve usability but aren't blocking (deposit forecasting, recurring auto-debits, group/chama-account UX, member-portal statement download).

This document maps the inventory, the benchmark, the wiring verification, and a prioritised recommendation list separating **Must Have (minimum to ship to real SACCOs)** from **Nice to Have**.

---

## 2. Current state — what's actually in the code

### 2.1 Deposits

**Backend**: `services/savings/internal/handler/deposit.go`, `deposit_executors.go`, `deposit_product.go`. Schema in `services/savings/internal/db/migrations/0002_deposits.up.sql` + BOSA/FOSA segmentation in `0027/0028_bosa_fosa_*.up.sql`.

**Tables**: `deposit_products`, `deposit_accounts`, `deposit_transactions`, `deposit_daily_balances`.

**Product types**: 8 (`ordinary, fixed, junior, holiday, goal, emergency, group, member_deposit`).
**Segments**: `bosa` (non-withdrawable) and `fosa` (withdrawable).
**Channels**: 6 (`cash, mpesa, airtel_money, bank_transfer, cheque, standing_order`).

**UI**: `web/admin/src/pages/Deposits.tsx`, `DepositProducts.tsx`.

**Confirmed working today** (from this session's prior fixes):
- Top-card totals include institutional members (counterparty_directory view used)
- Inline Deposit + Open + Withdraw all post receipts + GL via the `postingops` + `receiptops` seams
- BOSA lien enforcement on withdrawal
- M-PESA channel enum already includes `mpesa` (Phase 2/3 M-PESA work routes inbound paybill payments via the finance executor)

### 2.2 Shares

**Backend**: `services/savings/internal/handler/share.go`, `share_executors.go`. Schema in `0001_init.up.sql` (share_accounts, share_certificates, share_transactions) + `0011_share_approvals.up.sql`.

**Transaction types**: `purchase, transfer_in, transfer_out, bonus_issue, adjustment_credit, adjustment_debit, lien_placed, lien_released`.
**Multiplier basis (on loans)**: `none, shares, deposits, shares_plus_deposits` already encoded on `loan_products`.

**UI**: `web/admin/src/pages/Shares.tsx`.

**Confirmed working today**:
- Purchase posts receipts + GL via the consolidated approval flow
- Bonus issues fire workflow approvals (board level)
- Transfer between members exists
- Share certificate schema exists (numbered, prefix-configurable, supersedes chain)

### 2.3 Interest

**Backend**: `services/savings/internal/handler/interest.go`. Schema in `0003_interest.up.sql`.

**Tables**: `interest_runs` (with status lifecycle: draft → computing → preview → approved → posting → posted → locked), `interest_run_lines`, `tax_payable_ledger`.

**Configuration**: per-tenant `fy_start_month` + `fy_start_day` for FY anchoring. Per-product `interest_eligible` flag.

**Payout methods**: `credit_savings | buy_shares | external` (the three standard SACCO options).

**WHT**: `wht_rate_pct` snapshotted at run creation. Tax ledger tracks remittance.

**UI**: `web/admin/src/pages/InterestRuns.tsx`.

**Already shipped**:
- AGM-rate capture with resolution reference
- Weighted-average daily-balance computation
- Per-product opt-in via `interest_eligible`
- Run lifecycle with workflow integration
- GL posting via outbox

### 2.4 Dividends

**Backend**: `services/savings/internal/handler/dividend.go`, `dividend_offset.go`. Schema in `0004_dividends.up.sql` + `0035_dividend_run_je_link.up.sql`.

**Tables**: `dividend_runs` (similar lifecycle), `dividend_run_lines`.

**Calc methods**: encoded enum (`pct_of_share_capital`, others — confirm by reading 0004).

**UI**: `web/admin/src/pages/DividendRuns.tsx`.

**Already shipped**:
- AGM-rate capture
- Run lifecycle (draft → preview → approved → posted → locked)
- Multiple payout methods
- Line-level per-member adjustment
- GL posting via outbox
- **Dividend-offset policy plumbing in place** (from earlier Loans Phase 4 work) — `tenant_operations.dividend_offset_policy` enum with `disabled | manual_preview | automatic` modes

---

## 3. Benchmark — what established SACCOs offer

Below: feature-by-feature comparison against industry baseline. ✅ = already in nexusSacco; ⚠️ = partial / needs polish; ❌ = missing.

### 3.1 Deposits — feature parity

| Feature | Industry standard? | nexusSacco today |
| --- | --- | --- |
| Multiple deposit products (call/fixed/target/holiday/group/junior) | Yes | ✅ 8 types modelled |
| BOSA vs FOSA segmentation | Yes (SASRA-aligned) | ✅ done |
| Per-product fees (account maintenance, withdrawal charge) | Yes | ⚠️ fee_catalog exists for ad-hoc; per-product recurring fee is partial |
| Withdrawal notice period (e.g. 14-day notice for some products) | Yes | ✅ schema supports it |
| Standing orders (employer payroll deductions, recurring auto-debits) | Yes | ❌ only as a channel-name on a one-off receipt; no recurring schedule |
| Joint accounts (spouse, partner) | Common | ❌ not modelled |
| Junior accounts (minor with guardian) | Common | ✅ schema has `junior` product type + `guardian_member_id` |
| Group/chama accounts (organisation-as-owner) | Yes | ✅ `group` product + `group_org_id`; works via counterparty_directory |
| Goal-based savings with target date | Yes | ✅ `goal` product type with `goal_target_amount/date/description` |
| Mobile money sweeps (auto-pull from M-PESA on schedule) | Increasingly common | ❌ not modelled |
| M-PESA paybill direct routing | Required (modern) | ✅ Phase 2/3 of M-PESA work routes it |
| Member statement PDF (per account + consolidated) | Yes | ⚠️ `MemberStatementHandler` exists; PDF generation + download UI not confirmed |
| Email/SMS statement delivery | Yes | ❌ not wired |
| Real-time balance via SMS/USSD/portal | Common | ❌ no portal yet (member-portal is Phase 6 of Loans) |
| Loan-from-deposit offset (use deposits to offset overdue loan) | Common | ⚠️ partial — bosa_exit blocks on active liens; offset isn't a self-service action |
| Per-tenant deposit-channel charges (e.g. M-PESA pull fee) | Yes | ❌ not modelled |
| Deposit auto-renewal (fixed deposits matured roll forward) | Yes | ✅ `deposit_maturity_action` enum exists |
| Co-signatory / dual-control on large withdrawals | Yes (governance) | ✅ approval toggle exists |
| Reactivation flow for dormant accounts | Yes | ⚠️ status enum has `dormant`; reactivation workflow unclear |

### 3.2 Shares — feature parity

| Feature | Industry standard? | nexusSacco today |
| --- | --- | --- |
| Fixed par value | Yes (KES 100/200/1000 typical) | ✅ |
| Multiplier on loan eligibility (e.g. 3× shares) | Yes (SACCO core) | ✅ `loan_products.multiplier_basis + multiplier_value` |
| Minimum shareholding enforcement | Yes | ⚠️ schema supports it via `tenant_operations.min_shares_to_be_member` (confirm); enforcement unclear |
| Bonus issues from retained earnings | Yes (board-approved) | ✅ workflow integrated |
| Share transfers between members (typically on exit) | Yes | ✅ schema + handler |
| Share certificate generation (numbered, signed) | Yes (legal requirement) | ⚠️ schema exists; PDF generation UI unclear |
| Member share statement | Yes (annual at minimum) | ❌ not wired |
| Share lien for loan security | Yes | ⚠️ planned in Collateral Phase 1.5b (`collateral_share_pledges`) |
| Share buy from interest/dividend payout | Yes (payout method) | ✅ `buy_shares` payout method |
| Share buy via M-PESA paybill | Increasingly common | ⚠️ M-PESA Phase 3 distribution waterfall could route — confirm wired |
| Bonus issue proration for new members | Yes (governance) | ⚠️ check — does bonus issue exclude members who joined this FY? |
| Withdrawal of shares on exit (refund) | Yes (BOSA exit) | ✅ bosa_exit handler exists |
| Forfeiture rules (unsold shares on long-term default) | Rare but exists | ❌ not modelled |
| Share class differentiation (ordinary vs preferred) | Rare in SACCOs | n/a (not needed) |

### 3.3 Interest — feature parity

| Feature | Industry standard? | nexusSacco today |
| --- | --- | --- |
| AGM-declared annual rate | Yes | ✅ |
| Weighted-average daily balance computation | Yes (standard SACCO method) | ✅ |
| WHT withholding at 15% (Kenya resident rate) | Required | ✅ snapshotted at run creation |
| Tax remittance to KRA (iTax) tracking | Required | ⚠️ `tax_payable_ledger` exists; remittance report UI unclear |
| Per-product rate override (fixed deposits with contractual rate) | Yes | ⚠️ partial — `interest_eligible` is binary opt-out; product-specific rates not modelled in current run |
| Payout to savings | Yes | ✅ `credit_savings` payout method |
| Payout to buy shares | Yes | ✅ `buy_shares` |
| Payout to external (M-PESA / bank) | Yes (modern SACCOs) | ✅ `external` (M-PESA via B2C when wired) |
| Member-level interest projection / forecast | Nice to have | ❌ not modelled |
| Interest accrued YTD on member statement | Yes | ⚠️ depends on statement quality |
| Annual interest statement (sent to each member at year-end) | Yes | ❌ not wired |
| Per-member interest override (rare — for disputes) | Yes | ⚠️ `UpdateLinePayout` exists; per-member rate override unclear |
| Interest on dormant accounts (suspend or accrue) | Yes (policy) | ❌ policy not modelled |
| Reversal of a posted interest run | Yes (corrections) | ⚠️ status enum has cancelled but reversal of posted run unclear |

### 3.4 Dividends — feature parity

| Feature | Industry standard? | nexusSacco today |
| --- | --- | --- |
| AGM-declared dividend rate | Yes | ✅ |
| Computed on average share capital throughout FY | Yes | ⚠️ verify the calc method actually averages vs uses year-end snapshot |
| Proration for new members (joined mid-year get partial dividend) | Yes (governance) | ⚠️ check — depends on calc method implementation |
| WHT at 5% on dividends (Kenya resident rate) | Required | ⚠️ tax handling exists; confirm 5% vs 15% distinction |
| Payout methods (credit savings / buy shares / external) | Yes | ✅ |
| Dividend offset against loan arrears | Yes | ✅ policy plumbing in place (Loans Phase 4) |
| Member dividend statement (annual) | Yes (legal requirement) | ❌ not wired |
| Tax remittance to KRA | Required | ⚠️ same tax_payable_ledger; confirm dividend rate vs interest rate separation |
| Per-member dividend override / waiver | Yes (rare) | ✅ `UpdateLinePayout` exists |
| Dividend reinvestment program | Increasingly common | ✅ via `buy_shares` payout |
| Reversal of a posted dividend run | Yes (corrections) | ⚠️ unclear |
| Dividend proxy voting integration (member votes online) | Nice to have | ❌ governance feature, out of scope here |

---

## 4. Wiring verification — module integrations

### 4.1 Deposits ↔ Loans

| Integration | Expected | Verified? |
| --- | --- | --- |
| BOSA balance counts toward `multiplier_basis = 'deposits'` / `'shares_plus_deposits'` loan eligibility | Should sum BOSA segment | ⚠️ verify — does the multiplier query filter on `segment='bosa'` or count all deposits? Materially affects loan limits |
| BOSA lien on disbursement blocks withdrawal | Yes | ✅ enforced in `bosa_exit.go` |
| Loan disbursement to internal account (channel='internal') credits the deposit account | Yes | ✅ `disbursement_target_account_id` exists |
| Loan repayment via `auto_savings` channel debits deposit account | Yes (some products) | ✅ enum value exists; verify executor wired |
| Dividend offset against arrears uses BOSA / FOSA priority | Should drain FOSA first, then BOSA per policy | ⚠️ verify Loans Phase 4 implementation |

### 4.2 Shares ↔ Loans

| Integration | Expected | Verified? |
| --- | --- | --- |
| Share balance counts toward `multiplier_basis = 'shares'` / `'shares_plus_deposits'` | Should use current shares × par_value | ⚠️ verify what value is used (count vs value); per Kenyan SACCO norm: shares × par_value |
| Share pledge as collateral (loan security) | Yes | ⚠️ planned in Collateral Phase 1.5b |
| Loan written off → forfeit shares? | Policy-dependent (some yes, some no) | ❌ not modelled — needs decision |
| Share transfer blocked while member has active loan | Some SACCOs yes | ⚠️ verify policy |

### 4.3 Interest ↔ Finance/Tax

| Integration | Expected | Verified? |
| --- | --- | --- |
| Interest posting JE: DR Interest Expense (5000) / CR Member Savings + CR WHT Payable | Yes | ✅ posting present (verify exact CoA code mapping) |
| WHT in `tax_payable_ledger` | Yes | ✅ |
| Monthly tax remittance to KRA (iTax) — generates the remittance file | Yes | ❌ not wired — manual export today |
| Interest run reversal posts inverse JE + reverses tax ledger | Yes | ⚠️ verify reversal path |

### 4.4 Dividends ↔ Finance/Tax

| Integration | Expected | Verified? |
| --- | --- | --- |
| Dividend posting JE: DR Retained Earnings (3010) / CR Member Savings + CR WHT Payable (Dividend) | Yes | ⚠️ verify the debit account — should be Retained Earnings or Dividends Declared, not just Interest Expense |
| WHT rate of 5% (resident dividends) vs 15% (interest) | Required | ⚠️ verify dividend run uses correct rate, not the interest rate |
| Dividend offset → posts to loan accounts via repayment waterfall | Yes (per Phase 4) | ⚠️ verify Phase 4 implementation |
| Tax-exempt members (some institutional, religious orgs) | Yes (per Kenya tax law) | ❌ not modelled — needs `tax_exempt boolean` on counterparties |

### 4.5 Cross-module data consistency

These are correctness questions worth confirming with a small SQL audit script:

- For every active loan: `share_balance × par_value × multiplier_value ≥ approved_amount` (or equivalent with deposits if multiplier_basis allows). Any loan that breaches this is a data-integrity issue.
- For every member with an active loan: does `bosa_liens` have an active row equal to the BOSA balance at disbursement?
- For every interest run posted in the last year: does `SUM(interest_run_lines.gross_interest)` match `tax_payable_ledger` + `SUM(net_interest)` to the cent?
- For every dividend run posted in the last year: same reconciliation.
- For every member who was in arrears at last dividend run and the tenant's `dividend_offset_policy = 'automatic'`: does an offset posting exist?

A short reconciliation script (see Recommendation 5.5) surfaces any violations. Auditor would ask for exactly this.

---

## 5. Recommendations

Split into **Must Have (minimum)** — the items that must ship for nexusSacco to be acceptable to a real SACCO — and **Nice to Have** — standard polish that can wait.

### 5.1 MUST HAVE

#### Deposits

**M-D1. Member statement PDF + email delivery.** The `MemberStatementHandler` already exists; wire PDF generation (reuse `notification/internal/pdf`), and the email-delivery path through the notification service. SACCOs are required to provide quarterly statements; today this can't be done from the UI.

**M-D2. Standing order / scheduled deposit primitive.** A new `recurring_deposits` table with `frequency (weekly/monthly/quarterly), amount, target_account_id, source (payroll | mpesa_pull | manual), next_run_at, status (active/paused/cancelled)`. A daily cron processes due rows; ones marked `payroll` integrate with the existing salary check-off batch ingestion (Loans Phase 5); ones marked `mpesa_pull` queue STK Push requests (M-PESA Phase 7 follow-up). Required because payroll-driven deposits are the dominant funding mechanism for SACCOs.

**M-D3. Dormancy reactivation workflow.** Status `dormant` exists; reactivating requires officer review (KYC refresh + recent activity confirmation) and a workflow approval. Today it appears to be implicit on next deposit.

**M-D4. Joint accounts.** New `deposit_account_joint_owners (account_id, counterparty_id, signing_role)` table. Required for spouses / business partners; common request.

**M-D5. Per-product recurring fees.** The fee_catalog handles ad-hoc; per-product recurring (monthly maintenance, statement fees, withdrawal charges) needs a cron + posting path. Today fees are manual.

#### Shares

**M-S1. Share certificate generation UI.** The schema is there; build the PDF generation + download + email + reissue-on-balance-change flow. Legal requirement for many SACCOs.

**M-S2. Annual member share statement.** End-of-FY PDF showing opening shares, purchases, bonus issues, transfers, closing balance, par value, total worth. Required for audit + member transparency.

**M-S3. Bonus-issue proration for new members.** Verify the existing bonus-issue logic excludes members who joined after the relevant cutoff date, or include a settable cutoff. Today's bonus issues credit every active member equally — that's wrong for someone who joined three months before.

**M-S4. Minimum shareholding enforcement.** The setting exists (`tenant_operations.min_shares_to_be_member` — confirm); enforce it: members below the minimum get a `below_minimum_shares` flag visible in the register and a warning on the Member 360 page. Blocked from certain actions (loan application, share transfer) per tenant policy.

**M-S5. Share-lien wiring (linkage to Collateral Phase 1.5b).** When the Collateral Phase 1.5b PR lands, the `collateral_share_pledges` table connects shares pledged as loan security to actual share_accounts rows. Confirm this is in the merge plan.

#### Interest

**M-I1. Per-product interest rate override.** Today the run uses one AGM rate for all products. Fixed-deposit products typically have contractual rates that override the AGM rate. Add `interest_rate_pct_override` per product or per-run product line, and the compute engine applies it.

**M-I2. WHT remittance report (KRA iTax format).** Monthly export from `tax_payable_ledger` in the iTax CSV format. SACCOs are legally required to remit and file. Today manual export is the only path.

**M-I3. Interest reversal path.** A posted run must be reversible (corrections happen). Build a `reverse-run` endpoint that posts the inverse JE, reverses the tax ledger entries, sets status `reversed`. Workflow-gated.

**M-I4. Annual interest statement per member.** PDF showing daily balances, weighted average, gross interest, WHT, net, payout destination. Auditor + member requirement.

**M-I5. Dormancy interest policy.** Tenant setting `interest_on_dormant text DEFAULT 'accrue' CHECK (IN ('accrue','suspend'))`. Determines whether the compute engine includes dormant accounts. Today's behaviour is unclear and material to the totals.

#### Dividends

**M-V1. Dividend WHT rate distinction.** Confirm and enforce: 5% on dividends to residents (vs 15% on interest). Today both runs may use the same `wht_rate_pct` — must be different fields or different sources.

**M-V2. Dividend computed on average share capital, not year-end snapshot.** Verify and fix if needed — proration for new members hinges on this. The calc method enum probably has a `pct_of_share_capital`; confirm the underlying SQL uses a daily-weighted average across the FY, not just the closing balance.

**M-V3. Annual dividend statement per member.** Same shape as interest statement — required.

**M-V4. Dividend reversal path.** Same as M-I3.

**M-V5. Tax-exempt counterparty flag.** New column `counterparties.tax_exempt boolean DEFAULT false`. When true, both interest and dividend runs skip WHT for that counterparty. Required for institutional members (NGOs, religious organisations with KRA exemption certificates).

#### Wiring / consistency

**M-W1. Multiplier-basis correctness audit + fix.** Audit query confirming that for every active loan, the multiplier check holds using the correct values (share count × par_value vs share value vs deposits sum). Fix any discrepancies in the eligibility query before they cause approved-then-failed loans.

**M-W2. Interest/dividend reconciliation script.** A daily reconciler that asserts `SUM(run_lines.gross) = SUM(run_lines.net) + SUM(tax_payable_ledger.withheld)` per run. Fires an ops alert on mismatch.

**M-W3. Verify dividend-offset wiring** — confirm Loans Phase 4's plumbing actually runs when the policy is set and posts the expected offset JEs.

### 5.2 NICE TO HAVE

#### Deposits

- **N-D1. Mobile money sweep / STK Push auto-debit.** Wire recurring `mpesa_pull` source on the standing-orders table to STK Push (M-PESA Phase follow-up).
- **N-D2. Deposit projection / forecast.** Calculator showing "if you keep saving KES X/month, by date Y you'll have KES Z."
- **N-D3. Withdrawal calendar.** UI showing scheduled withdrawals (notice-period queue + maturity dates) per member.
- **N-D4. Self-service deposit modification.** Member can change their standing-order amount via portal (Phase 6 of Loans member-portal work).
- **N-D5. Group account roll-up reporting.** Single dashboard showing total of all member contributions to a chama account.

#### Shares

- **N-S1. Share buy via M-PESA paybill direct routing.** Already supported by Phase 3 of M-PESA waterfall (shares is one of the routable destinations).
- **N-S2. Share forfeiture rules.** Policy + workflow for long-default loans where shares are forfeited and credited to the SACCO retained earnings. Rare but auditor-relevant.
- **N-S3. Share-transfer reasons taxonomy.** Track transfer reasons (member exit, family transfer, gift, etc.) for SASRA reporting.
- **N-S4. Share certificate physical-print queue.** For SACCOs that still issue paper certificates — a daily print queue with mailing-address sticker generation.

#### Interest

- **N-I1. Member interest projection (year-to-date earned, projected year-end).** A member-portal tile + admin view.
- **N-I2. Multi-rate within a run (tiered).** Some products may want a tiered rate based on balance bands. Rare; could be added later.
- **N-I3. Per-member rate override / dispute resolution workflow.** A formal dispute → adjustment → posting flow.

#### Dividends

- **N-V1. Dividend proxy voting integration.** Members vote online for AGM resolutions including the dividend rate.
- **N-V2. Dividend reinvestment program (DRIP) UX.** Member opts in to "always buy shares with my dividend" — sticky preference on counterparty.
- **N-V3. Special-distribution dividends.** One-off bonus distributions outside the FY cycle.

#### Cross-module

- **N-W1. Combined annual financial statement.** One PDF per member at year-end combining: share statement + deposit statement + loan statement + interest earned + dividend earned. Standard at modern SACCOs.
- **N-W2. SACCO-wide annual report PDF generator.** End-of-year branded PDF with portfolio metrics, member counts, dividend declarations, audited highlights. AGM-ready.

---

## 6. Priority phasing — proposed sequencing

Five phases, smallest to largest. Each shippable independently.

### Phase 2.1 — Statements + WHT remittance (MUST)

Wire the four annual/quarterly statements (deposit, share, interest, dividend) as PDF generation, surface in admin UI + send via email. Add WHT iTax remittance report export. Smallest scope, biggest auditor + member impact. **M-D1, M-S2, M-I2, M-I4, M-V3**.

### Phase 2.2 — Standing orders + dormancy + joint accounts (MUST)

The operational backbone: recurring deposits, dormancy reactivation flow, joint account model. **M-D2, M-D3, M-D4, M-D5**.

### Phase 2.3 — Share polish + certificate generation (MUST)

Share certificate PDF, bonus-issue proration verification + fix, minimum-shareholding enforcement, confirm share-lien linkage with Collateral 1.5b. **M-S1, M-S3, M-S4, M-S5**.

### Phase 2.4 — Interest + Dividend correctness (MUST)

Per-product interest rate override, dividend WHT rate fix, dividend-on-average-capital verification, both runs reversibility, tax-exempt flag, dormancy interest policy. **M-I1, M-I3, M-I5, M-V1, M-V2, M-V4, M-V5**.

### Phase 2.5 — Wiring + reconciliation (MUST)

Multiplier-basis audit, interest/dividend reconciliation worker, dividend-offset end-to-end verification. **M-W1, M-W2, M-W3**.

### Phase 2.6 — Nice to have (later)

Mobile money sweeps, deposit forecasting, member-portal pieces, special distributions, combined annual statement, DRIP. Defer until 2.1-2.5 ship.

---

## 7. What I need from you

1. **Scope confirmation.** All MUST items in section 5.1 — agree, or drop any?
2. **Phasing.** Five sub-phases as in section 6 — agree, or re-prioritise?
3. **Anything missing.** Features I haven't surfaced that you've seen at other SACCOs?
4. **Wiring audits.** Want me to also write a small SQL diagnostic script (no migrations, just `SELECT` queries) you can run against tujenge today to confirm the wiring questions (4.1, 4.2, 4.3, 4.4, 4.5) before we commit to fix prompts? This way we know which "verify" items in section 4 are actually broken vs already correct.
5. **Decisions deferred to implementation.** Any of these you want my recommendation on:
   - Tax-exempt detection — flag on counterparty (per-member) or per-tenant config of exempt member kinds?
   - Joint accounts — equal signing rights (any owner withdraws) or per-rule (must have N signatures)?
   - Standing-order failure handling (M-PESA STK fails, payroll file row missing) — retry policy + member notification?
   - Share forfeiture (Nice to Have) — keep deferred?

Once you reply, I'll write the implementation prompts per phase. If you want the diagnostic SQL first (point 4), say so — I'll write that as a single-paste script before the prompts.