# Phase 2.1 — Statements + WHT remittance

Wire the four annual/quarterly statements (deposit, share, interest, dividend) as PDF generation surfaced in the admin UI + email-delivery path. Add the WHT iTax remittance report export. Smallest of the five sub-phases, biggest auditor + member impact. Items: **M-D1, M-S2, M-I2, M-I4, M-V3**.

## Files to read first

- `services/savings/internal/handler/member_statement.go` — existing handler that's the seed for the deposit statement.
- `services/notification/internal/pdf/` — PDF generation infrastructure used by collections letters; reuse for statements.
- `services/notification/internal/smtp/` — email send used for invites; reuse for statement delivery.
- `services/savings/internal/db/migrations/0003_interest.up.sql` (interest_runs + interest_run_lines + tax_payable_ledger) and `0004_dividends.up.sql` (dividend_runs + dividend_run_lines) — the data the statements consume.
- `services/savings/internal/db/migrations/0001_init.up.sql` (share_accounts + share_transactions + share_certificates) — share statement data.
- `services/savings/internal/db/migrations/0002_deposits.up.sql` (deposit_accounts + deposit_transactions + deposit_daily_balances) — deposit statement data.

## Scope — one PR, six steps

### Step 1 — Statement templates

Create PDF templates per statement kind under `services/notification/internal/pdf/templates/statements/`:

- `deposit_statement.html` — per-account or consolidated. Sections: header (tenant branding, period, member info), opening balance, transactions table, closing balance, footer (signature line for officer + tenant disclaimer).
- `share_statement.html` — opening shares, purchases, bonus issues, transfers, closing balance, par value, total worth, current pledged shares.
- `interest_statement.html` — header (FY, AGM rate, run reference), per-account weighted-average daily balance, gross interest, WHT withheld, net interest, payout method + destination, run-level totals.
- `dividend_statement.html` — header (FY, AGM rate, run reference), average share capital, gross dividend, WHT withheld, net dividend, payout method + destination.

Each template takes a typed struct so renderer + tests are deterministic. Reuse the templating library already in `notification/internal/pdf/templates/collections/`.

### Step 2 — Statement endpoints

Add to `services/savings/internal/handler/`:

| Endpoint | Permission |
| --- | --- |
| `GET /v1/members/{cp_id}/statements/deposits.pdf?from=&to=&account_id?=` | `members:view` (officer) OR self (member via portal) |
| `GET /v1/members/{cp_id}/statements/shares.pdf?fy=` | same |
| `GET /v1/members/{cp_id}/statements/interest.pdf?fy=` | same |
| `GET /v1/members/{cp_id}/statements/dividend.pdf?fy=` | same |
| `POST /v1/members/{cp_id}/statements/email` | `members:view` + `member:contact` permission. Body `{kind: 'deposits'|'shares'|'interest'|'dividend', period: '...'}`. Renders + emails to member's registered email. |

Cache each rendered PDF for 24h keyed on `(member_id, kind, period, max_underlying_txn_at)` — re-clicks don't regenerate.

Audit log: `statement.generated` event with kind + period.

### Step 3 — WHT iTax remittance export

New endpoint `GET /v1/tax/wht-remittance.csv?period=YYYY-MM&tax_type=interest|dividend|both` and a matching admin UI page at `/accounting/wht-remittance`. Returns the KRA iTax-formatted CSV:

```csv
member_no,full_name,kra_pin,gross_amount,wht_rate_pct,wht_withheld,net_amount,run_no,posted_at
M-2024-00123,Eric Warui,A012345678X,10000.00,15.00,1500.00,8500.00,IR-2026-00001,2026-06-30
```

Per Kenyan iTax requirements:

- Resident interest: WHT 15% — withholding on individuals, exempt members excluded
- Resident dividends: WHT 5% — withholding on individuals, exempt members excluded
- Non-resident rates: separately tracked (rare for SACCOs; flag for future enhancement)
- The `tax_payable_ledger` rows are the source of truth — they were captured at run-post time with the correct rate snapshot
- Excludes counterparties flagged `tax_exempt = true` (per the locked decision in §9 of the benchmark; the column is added in Phase 2.4 so this PR should accept its absence gracefully — if the column doesn't exist yet, assume all counterparties are non-exempt)

UI page:

- Period picker (defaults to last completed month)
- Tax type filter (Interest only / Dividend only / Both)
- "Generate CSV" button → downloads the file
- "Generate management report PDF" button — summary view (gross, withheld, count of payees) for officer review before submission
- Historical table of past remittance exports (for audit chain)

Audit: `tax.wht_remittance.exported` event with period + kind.

### Step 4 — Bulk statement generation cron

Many SACCOs send statements quarterly to every member. Add `services/savings/cmd/statement-mailer` worker:

- Runs on tenant-configurable schedule (cron expression in `tenant_operations.statement_mail_cron text default '0 6 1 */3 *'` — 6am on the 1st of every 3rd month)
- For each tenant + each member with `notification_preferences.statement_email = true`: render deposits + shares + interest (if FY in scope) + dividend (if applicable) statements, package as separate PDF attachments on one email
- Idempotent on `(tenant_id, member_id, period)` via new `statement_mailings` table — re-runs same period are no-ops
- Failure path: bounced emails captured + flagged for officer review

Schema:

```sql
CREATE TABLE statement_mailings (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id uuid NOT NULL,
  period_label    text NOT NULL,
  email_address   text NOT NULL,
  statement_kinds text[] NOT NULL,
  sent_at         timestamptz NOT NULL DEFAULT now(),
  bounce_status   text,
  UNIQUE (tenant_id, counterparty_id, period_label)
);
```

### Step 5 — Member preferences

New table `member_notification_preferences` (or extend if it exists):

```sql
CREATE TABLE member_notification_preferences (
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  counterparty_id    uuid NOT NULL PRIMARY KEY,
  statement_email    boolean NOT NULL DEFAULT true,
  statement_sms      boolean NOT NULL DEFAULT false,
  transaction_alerts boolean NOT NULL DEFAULT true,
  marketing          boolean NOT NULL DEFAULT false,
  preferred_language text NOT NULL DEFAULT 'en' CHECK (preferred_language IN ('en','sw')),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
```

Admin UI: on each member profile, add a "Notification preferences" section. Officer can edit on behalf of member (audit-logged). Phase 6 (Portal) lets the member edit it themselves.

### Step 6 — UI integration

Per-member statements:

- On `/members/{id}` (CounterpartyProfile), add a "Statements" tab (or extend existing Documents tab — pick one; recommend new tab to keep Documents focused on uploaded files vs generated artifacts).
- The tab has four cards (Deposits / Shares / Interest / Dividend). Each card has a period picker + Generate PDF button + Email to member button.
- WHT remittance lives under `/accounting/wht-remittance` per Step 3.

### Step 7 — Tests + observability

Go:

- `statement_pdf_test.go` — render each statement against fixture data; assert key fields appear in the PDF; assert idempotent re-runs.
- `wht_remittance_test.go` — CSV columns match the iTax format; only non-exempt counterparties appear; rate is correct per kind (15% interest / 5% dividend).
- `statement_mailer_test.go` — idempotent per period; bounced emails flagged.

React:

- `StatementsTab.test.tsx` — period picker behavior; download triggers.
- `WHTRemittancePage.test.tsx` — CSV export + management PDF.

Audit + metrics:

- Events: `statement.generated`, `statement.emailed`, `tax.wht_remittance.exported`, `statement_mailing.sent/bounced`.
- Metrics: `statements_generated_total{kind}`, `wht_remittance_exports_total`, `statement_mailings_total{status}`.

## Acceptance walkthrough

1. Open a member with both interest and dividend posted in last FY. On their profile → Statements tab. Generate Deposits statement for last quarter → PDF downloads. Cover page shows tenant branding, member name, period, opening balance, transactions, closing balance.
2. Generate Shares statement for FY 2025-2026 → PDF shows opening shares, purchases, bonus issues, transfers, closing balance, par value, total worth.
3. Generate Interest statement for FY 2025-2026 → shows weighted balances, gross interest, WHT, net, payout destination per account.
4. Generate Dividend statement for FY 2025-2026 → shows average share capital, gross dividend, WHT, net.
5. Click "Email to member" on the deposit statement → member's registered email receives the PDF as attachment within 1 minute. Audit log records the send.
6. Open `/accounting/wht-remittance` → period = 2026-06 → Generate CSV. File opens in Excel with iTax-format columns. Spot-check: a member flagged `tax_exempt` (if column exists from Phase 2.4) does not appear.
7. Run the statement mailer for tenant=tujenge, period=2026-Q2. Members with `statement_email = true` receive their statements. `statement_mailings` table has one row per (member, period). Re-run — no duplicates.

## Idempotency / safety

- Statement PDFs are cached 24h per `(member_id, kind, period, max_txn_at)`.
- `statement_mailings` table prevents duplicate sends per period.
- The WHT remittance CSV is read-only — no mutations.
- All new tables RLS-scoped on `tenant_id`.
- `gofmt`, `go vet`, full `go test ./services/savings/... ./services/notification/...`, `pnpm test`, `pnpm build` all green.

When you're done, paste into the PR description: one screenshot of each statement PDF + the WHT remittance CSV header + first 3 data rows + the diff stat.