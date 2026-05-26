# Institutional members are missing from Shares & Deposits — fix the list ↔ totals ↔ GL gap

## Symptom (what Mike sees)

1. **Shares page** (`web/admin/src/pages/Shares.tsx`)
   - The "Share register" list shows individual members only — institutional members never appear.
   - Adding up the visible rows does not equal the "Total share capital" KPI in the top card.
   - The same figure never lands on Accounting → Balance Sheet or on the SASRA return.
2. **Deposits page** (`web/admin/src/pages/Deposits.tsx`)
   - Same pattern. The list shows individuals only; the top-card "Total balance" KPI is bigger than the visible rows; the figure does not reach Balance Sheet / SASRA.

Both pages drive their list off endpoints that silently filter the data, while their KPI cards aggregate the un-filtered table. Reports drive off the General Ledger, which is missing some of the postings entirely.

## Root cause (already verified in code)

### A. The list endpoints filter institutional counterparties out

`share_accounts` and `deposit_accounts` are keyed on **`counterparty_id`** (universal identity that covers both individuals and organisations). The summary endpoints aggregate those tables directly — institutional rows are included.

The list endpoints, however, inner-join `members`, which only contains **individual** counterparties (institutional CPs live in `org_members`). The join silently drops every institutional row from the list and from `total`:

- `services/savings/internal/store/share_store.go`
  - `ListAccountsTx` (lines 314–374) — `JOIN members m ON m.counterparty_id = a.counterparty_id`
  - `ActiveAccountsTx` (lines 468–489) — same join — institutional members **also get skipped on bonus-share runs**.
- `services/savings/internal/store/deposit_store.go`
  - `ListAccountsTx` (lines 207–276) — `JOIN members m ON m.counterparty_id = a.counterparty_id`.
- Summary counterparts (`SummaryTx` in both files) read the base tables directly → totals include institutions.

Same pattern leaks elsewhere — fix those at the same time:

- `services/savings/internal/store/dividend_store.go` lines 300, 350, 392 (eligibility queries → institutional members never receive dividends).
- `services/savings/internal/store/loan_store.go` line 195 (loan list page hides institutional borrowers).
- `services/savings/internal/store/loan_reports_store.go` lines 328, 392, 552, 599 (arrears + portfolio reports).
- `services/savings/internal/store/loan_collections_store.go` line 147 (collections report).
- `services/savings/internal/store/loan_guarantee_store.go` line 147 (guarantee linkage).

### B. The numbers never reach the GL

Even after the list bug is fixed, the totals on top of the page still won't equal anything on the Balance Sheet or SASRA return because several money movements never post to the General Ledger. Those gaps are covered in detail by the existing `nexusSacco_Finance_Module_Recommendations.md` file (R1–R6) — that work needs to ship for the reports to come alive. The list fix below is necessary but **not sufficient** on its own; institutional capital still won't show on the Balance Sheet until R2/R5 (atomic GL posting for shares + deposits) land.

The third deficit is institution-specific: institutional applications do not auto-open share / deposit accounts (`activateInstitutionalTx` in `services/member/internal/store/application_store.go` lines 866–939 — it deliberately skips the share/deposit open). When an officer later opens an account on the org profile, the standard open / deposit / share-purchase handlers handle it — but only if they actually call into the GL. R2 and R5 in the finance recommendations make those paths atomic & complete; this prompt assumes those land or are shipped in parallel.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo (multi-tenant Go services + a React admin). The Shares and Deposits pages hide institutional members and their totals reconcile to nothing on the Balance Sheet / SASRA report. Fix the list ↔ totals ↔ ledger gap. Treat this as one self-contained change.
>
> **Scope**
>
> 1. Make every account listing (shares, deposits, loans, dividend / bonus eligibility, loan reports, collections, guarantees) include institutional counterparties.
> 2. Reconcile the top-card KPI to the rows that the list actually returns.
> 3. Make institutional share / deposit movements show up on the Balance Sheet and SASRA return via the same posting path individuals already use.
> 4. Add regression tests so the gap can't reopen.
>
> **Background you must read first**
>
> - `services/savings/internal/store/share_store.go` — `ListAccountsTx` (≈314), `SummaryTx` (≈427), `ActiveAccountsTx` (≈468).
> - `services/savings/internal/store/deposit_store.go` — `ListAccountsTx` (≈207), `SummaryTx` (≈694).
> - `services/savings/internal/store/dividend_store.go` (≈284–400), `loan_store.go` (≈188), `loan_reports_store.go`, `loan_collections_store.go`, `loan_guarantee_store.go`. Every `JOIN members m ON m.counterparty_id = ...` in these files is suspect.
> - `services/member/internal/db/migrations/0007_counterparties.up.sql` — confirms `counterparties` is the universal identity, `kind ∈ {individual, institution}`, with `display_name`, `cp_number`, `status`. `members` and `org_members` both FK to it 1:1.
> - `services/member/internal/store/application_store.go` lines 866–939 — institutional activation creates an `org_members` row but **does not open share / deposit accounts**; that has to happen explicitly on the org profile. Don't break that path.
> - `services/savings/internal/handler/deposit.go` `OpenAccount` (≈130) — accepts any `counterparty_id`, so institutional accounts are already openable through the API once the UI exposes the action.
> - `web/admin/src/pages/Shares.tsx` and `web/admin/src/pages/Deposits.tsx` — the two screens whose KPIs / lists need to reconcile.
> - `nexusSacco_Finance_Module_Recommendations.md` (root of repo) — R2 (atomic deposit/withdrawal GL posting) and R5 (full share-movement GL coverage). This prompt assumes those land in the same release; coordinate the SQL migration numbers and posting-rule changes so the two PRs merge cleanly.
>
> **Migration: add a thin `counterparty_directory` view**
>
> Add a new migration in `services/savings/internal/db/migrations/` (next sequential number — at time of writing `0024_counterparty_directory.up.sql`) that creates a read-only view over `counterparties` returning every column the list queries need, regardless of `kind`:
>
> ```sql
> CREATE OR REPLACE VIEW counterparty_directory AS
> SELECT
>   c.id                    AS counterparty_id,
>   c.tenant_id,
>   c.kind,                 -- 'individual' | 'institution'
>   c.cp_number,
>   c.display_name          AS full_name,
>   c.status::text          AS cp_status,
>   COALESCE(m.member_no, om.org_no, c.cp_number) AS member_no,
>   CASE c.kind WHEN 'institution' THEN true ELSE false END AS is_institution,
>   m.id                    AS member_id,    -- nullable
>   om.id                   AS org_id        -- nullable
> FROM counterparties c
> LEFT JOIN members      m ON m.counterparty_id  = c.id
> LEFT JOIN org_members  om ON om.counterparty_id = c.id;
> ```
>
> The view must respect the existing RLS policy on `counterparties` — confirm `current_tenant_id()` filters propagate (write a quick test in `share_store_test.go`).
>
> **Rewire every offending query**
>
> Replace every `JOIN members m ON m.counterparty_id = a.counterparty_id` in the files listed above with:
>
> ```sql
> JOIN counterparty_directory cd ON cd.counterparty_id = a.counterparty_id
> ```
>
> ...and project `cd.member_no`, `cd.full_name`, `cd.cp_status`, `cd.kind`, `cd.is_institution`. **Do not** restrict `cd.kind` — institutional rows must be returned. Update the search predicate to use `cd.full_name`, `cd.member_no`, and `cd.cp_number`.
>
> For the bonus / dividend eligibility joins, replace `m.status NOT IN ('blacklisted', 'exited', 'deceased', 'rejected')` with the equivalent `counterparties.status` predicate (`cd.cp_status NOT IN ('inactive', 'closed', 'rejected')` — confirm the exact set against `counterparty_status` enum and align with the prior `members.status` semantics; if `org_members.status` has its own excludes, OR them in).
>
> Update the corresponding `AccountListItem` / `AcctListItem` structs to expose two new fields on the JSON response:
>
> - `kind: "individual" | "institution"`
> - `is_institution: boolean`
>
> Mirror those in `web/admin/src/api/client.ts` (`ShareAccountListItem`, `DepositAcctListItem`).
>
> **Reconcile the top-card to the visible rows**
>
> The list endpoints accept a search `q` and (for shares) a `below_min` filter; the top-card endpoints today ignore those. Fix one of two ways — pick (a) unless the UX team objects:
>
> (a) Have the list endpoint also return aggregate fields scoped to the current filter — e.g. `{ items: [...], total: N, totals: { shares_held, share_capital, accounts_with_lien, ... } }`. Render `totals` next to the list header ("Showing 24 of 312 accounts · KES 1,840,000 of KES 17,400,000 total"). The top-card KPI keeps reading from `SummaryTx` (which is correctly tenant-wide and now correctly includes institutional rows).
>
> (b) Alternative: pass the same filters into `SummaryTx`. Cleaner but more API surface — only do this if the design language already implies the top-card moves with filters.
>
> Either way, every numeric on the page MUST be derived from the same SQL, so an institutional account that was opened today shows up in both the row total and the headline KPI.
>
> **UI surfacing of institutional rows**
>
> In `web/admin/src/pages/Shares.tsx` and `Deposits.tsx`:
>
> - Show a small `Org` chip next to the name when `is_institution` is true (reuse the existing `Badge` / `StatusBadge` styling).
> - The "Open" / row link currently goes to `/members/{counterparty_id}?tab=accounts`. Route institutional rows to the org profile instead — confirm the org-profile path and use it (`/organizations/{counterparty_id}` or whatever the canonical route is in `App.tsx`). Don't 404 on org rows.
> - Add a filter chip "Member type · individual / institution / all" so cashiers can split the view if they want. Default = all.
>
> **Ledger coverage for institutional accounts**
>
> Confirm — and write a Go acceptance test for — that opening a deposit account for an institutional counterparty and posting an opening deposit produces the same GL entries (DR cash 1000/1020/1030/1040, CR liability 2000/2100/2200/2300 depending on product type) as it does for an individual. Mirror that for share purchases (DR cash, CR 3000 Member Share Capital). The posting paths are keyed on `counterparty_id`, not member kind, so they should already work — the test exists to prevent regressions.
>
> Put the tests in:
> - `services/savings/internal/handler/deposit_institutional_gl_acceptance_test.go`
> - `services/savings/internal/handler/share_institutional_gl_acceptance_test.go`
>
> Each test should:
> 1. Insert a `counterparties` row with `kind='institution'` + an `org_members` shell.
> 2. Hit the open + opening-deposit / share-purchase HTTP endpoints.
> 3. Query `journal_entries`/`journal_lines` and assert exactly the expected balanced post — including against `chart_of_accounts.code`, not internal ids.
> 4. Hit `/v1/reports/balance-sheet` and assert "Member Share Capital" / "Ordinary Savings Deposits" reflect the new amount.
>
> **SASRA cross-check**
>
> Add one more assertion: hit `/v1/reports/sasra-return` for the entry's period and confirm institutional deposits roll into `total_deposits` / `core_capital` denominators correctly (whatever the relevant ratio is — read `services/accounting/internal/store/report_store.go` `SASRAReturnTx`). If they do not — because the report filters by `members` somewhere — fix that too and add the same assertion.
>
> **Reconciliation report**
>
> Add a thin admin-only endpoint `GET /v1/accounting/subledger-recon` (reuses R8 from the finance recommendations) — or, if R8 has already landed, extend its `share_capital` and `member_deposits` rows to compare `SUM(share_accounts.shares_held * par_value)` and `SUM(deposit_accounts.current_balance)` against the GL closing balance for the matching accounts. The view should surface mismatches per kind (individual / institution) so the books gap is visible from one page.
>
> **Acceptance walkthrough**
>
> 1. As a tenant admin, navigate Members → Organizations, open an institutional member, open a share account, purchase 10 shares of par 1,000.
> 2. Navigate Shares — the org appears in the register with an "Org" chip; the row's capital column shows KES 10,000; the visible-rows total bar shows the org's contribution; the top-card "Total share capital" KPI moved by exactly 10,000.
> 3. Navigate Accounting → Balance Sheet (today) — "Member Share Capital" moved by 10,000.
> 4. Navigate Accounting → SASRA return — share capital / core capital denominators reflect the change.
> 5. Repeat steps 1–4 with a BOSA deposit of KES 50,000 — the Deposits page, Balance Sheet "Ordinary Savings Deposits", and SASRA `total_deposits` all move by 50,000.
> 6. Subledger recon (`/v1/accounting/subledger-recon`) shows `share_capital` and `member_deposits` rows reconciled to zero variance, with the institutional contribution attributed correctly.
>
> **Idempotency / safety rules**
>
> - View migration is `CREATE OR REPLACE` + a matching `DROP VIEW` in the `.down.sql`.
> - No table-shape changes — keep this PR purely additive.
> - Do not touch `members` or `org_members`. The view is the seam.
> - Every changed query keeps its existing `LIMIT/OFFSET`; pagination semantics are preserved.
> - RLS: confirm via `EXPLAIN (ANALYZE, VERBOSE)` against a two-tenant fixture that the view does not leak across tenants.
> - Run `gofmt`, `go vet`, `go test ./services/savings/... ./services/accounting/...` before opening the PR.
>
> When you're done, paste the diff stat and the list of GL accounts touched by the new institutional acceptance tests into the PR description.

---

## Why this is the right shape

`counterparty_directory` becomes the single canonical join target for any "member or org" listing. It costs nothing at runtime (Postgres expands the view; planner sees the same plan), it avoids a giant set of `LEFT JOIN members ... LEFT JOIN org_members ...` blocks scattered across the codebase, and it gives the next engineer one obvious place to add columns the UI needs (next-of-kin, KYC state, contact info, etc.) without re-touching every store.

The list / total reconciliation is the second leg — institutional accounts being visible doesn't help if "Total share capital" still over-reports versus "showing 24 of N accounts". Same query, same numbers.

The third leg — Balance Sheet & SASRA — depends on R2 and R5 from the existing recommendations doc. This prompt is a clean add-on to that work, not a replacement.