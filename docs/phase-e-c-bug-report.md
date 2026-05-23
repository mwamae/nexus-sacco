# nexusSacco — Phase E C Bug Report & Claude Code Fix Prompts

**Tester:** Claude (systems analyst pass)
**Branch:** `phase-e-c-housekeeping`
**Tenant tested:** `tujenge.nexussacco.local:5173` (user: `wamaemikke@gmail.com`, tenant_owner + 8 staff roles)
**Platform admin tested:** `platform.nexussacco.local:5173` (`admin@nexussacco.local`)
**Date:** 2026-05-23

---

## Top-line conventions for every fix

> The user wants:
>
> 1. The **code** (Go fields, SQL columns, internal JSON keys, TypeScript types) to use `counter_party` / `counterparty`. That refactor is in flight and the canonical FK is now `counterparty_id`.
> 2. The **UI text** the human reads — page titles, table headers, button labels, empty-state copy, error messages, breadcrumbs, audit log copy — must say **“Member”** (or “Members”). Never “Counterparty”, “CP #”, “Counterparties”, “Onboard counterparty”, etc.
> 3. URL paths in the browser stay `/members/<id>` and `/orgs/<id>` (already correct in `App.tsx`).
>
> Apply this convention to every fix below.

---

## A. Confirmed P0 backend 500s (every list endpoint that joins to `members`)

Caught by hitting these endpoints with a valid bearer token from a logged-in tenant session. **All five return 500 `{"error":{"code":"internal_error","message":"an unexpected error occurred"}}`** even though the underlying SQL looks roughly right on inspection. They make most of the UI unusable today:

| Endpoint | Page broken |
|---|---|
| `GET /api/v1/applications?limit=10`     | `/applications` — “an unexpected error occurred” |
| `GET /api/v1/loan-applications?limit=10`| `/loans` (Applications tab) |
| `GET /api/v1/loans?limit=10`            | `/loans` and member profile Accounts > Loans |
| `GET /api/v1/share-accounts?limit=10`   | `/shares` |
| `GET /api/v1/deposit-accounts?limit=10` | `/deposits` |

These are not network/auth failures (counterparties list, products, dividend-runs, interest-runs, /loans/arrears-summary all return 200 with the same token). The shared symptom is "SELECT-then-scan against a table that just had columns/triggers rewired in migration 0021 (savings) / 0012 + uncommitted 0013 + 0014 (member) — the running Go binaries are almost certainly built from a pre-rename version while the DB schema has moved on".

Working-tree state corroborates this: `git status` shows
```
modified:   services/member/internal/domain/application.go
modified:   services/member/internal/store/application_store.go
modified:   web/admin/src/api/client.ts
modified:   web/admin/src/pages/Applications/ApplicationDetail.tsx
Untracked:  services/member/internal/db/migrations/0013_status_mirror_trigger.{up,down}.sql
Untracked:  services/member/internal/db/migrations/0014_drop_materialized_member_org.{up,down}.sql
```
i.e. the user has dropped `materialized_member_id` / `materialized_org_id` from `membership_applications` in the DB (or is about to), and is mid-flight rewriting `appCols` to use `materialized_counterparty_id` instead — but the binaries currently running may still be selecting the old column names.

## B. Confirmed P1 backend route mismatch

`GET /api/v1/members/<counterparty.id>` returns 404 `{"error":{"code":"not_found","message":"member not found"}}` and 400 `invalid member id` for shorter inputs. The frontend's `CounterpartyProfile.tsx` page (`getMember(id)`) is mounted on `/members/:id` and `/orgs/:id`, and it correctly receives `legacy_target_id` from `Members.tsx`'s row mapper — **but if `legacy_target_id` is null (any counterparty created post Phase B without a bridged member/org row), the page hits `/v1/members/<counterparty.id>` and renders “Couldn't load counterparty”**. The Phase D Test Member (CP-2026-00011) is the canary on tujenge.

## C. Silent bug in `loan_guarantee_store.go` JOIN

`services/savings/internal/store/loan_guarantee_store.go:147`:

```go
JOIN members          m ON m.id = a.counterparty_id
```

`a.counterparty_id` references `counterparties(id)`, not `members(id)`. This JOIN can only succeed if a counterparty UUID happens to collide with a member UUID — i.e. **it returns zero rows in production**, so the guarantor inbox / “loans I’m guaranteeing” view is always empty even when the borrower has guarantors.

## D. Silent fidelity bug: every savings/loan list silently drops org-owned rows

Every list query in `services/savings/internal/store/*.go` is an **INNER JOIN** to `members`:

```sql
FROM loans l
JOIN members m ON m.counterparty_id = l.counterparty_id
```

`members` is individual-only. Any loan / deposit account / share account / loan_application whose `counterparty_id` is an institution (chama, company, ngo…) will **silently disappear from lists**, including from arrears reports and dividend runs. Same pattern in `loan_store.go`, `loan_application_store.go`, `deposit_store.go`, `share_store.go`, `dividend_store.go`, `loan_reports_store.go`, `loan_collections_store.go`, `member_statement_store.go`, `provisioning_store.go`.

## E. UI strings that leak “counterparty” to end-users (must say “Member”)

| File | Line(s) | Current text | Should be |
|---|---|---|---|
| `web/admin/src/pages/Members.tsx` | 118 | `Onboard counterparty` | `Onboard member` |
| `web/admin/src/pages/Members.tsx` | 205 | `<th>CP # / Legacy</th>` | `<th>Member # / Legacy</th>` |
| `web/admin/src/pages/Members.tsx` | (KIND column header) | `CP # / LEGACY` is rendered into the table head | Same as above |
| `web/admin/src/pages/CounterpartyProfile.tsx` | 111 | `No such counterparty.` | `No such member.` |
| `web/admin/src/pages/CounterpartyProfile.tsx` | 112 | `errorTitle="Couldn't load counterparty"` | `Couldn't load member` |
| `web/admin/src/pages/CounterpartyProfile.tsx` | 115 | `We couldn't fetch this counterparty's profile.` | `We couldn't fetch this member's profile.` |
| `web/admin/src/pages/CounterpartyProfile.tsx` | 196 | `ariaLabel="Counterparty sections"` | `Member sections` |
| `web/admin/src/pages/CounterpartyProfile.tsx` | 655 | `Audit log entries for this counterparty.` | `Audit log entries for this member.` |
| `web/admin/src/pages/Applications/ApplicationDetail.tsx` | 134 | `<Field label="Counterparty ID" ...>` | `<Field label="Member ID" ...>` |
| `web/admin/src/pages/Applications/ApplicationDetail.tsx` | 137 | `Open counterparty →` button label | `Open member →` |

Type names (`Counterparty`, `CounterpartyKind`, `CounterpartyView`, `CounterpartyShell`, `CounterpartyProfile`) **stay as-is** — they’re code identifiers and follow rule (1).

## F. Other stale frontend references to dropped fields

| File | Line | Issue |
|---|---|---|
| `web/admin/src/pages/Loans.tsx` | 715 | `<a href={`/members/${g.guarantor_member_id}`}>{g.guarantor_member_id.slice(0,8)}…</a>` — `guarantor_member_id` was renamed to `guarantor_counterparty_id` on the savings side (migration 0021) but the TS still reads `guarantor_member_id`. |
| `web/admin/src/api/client.ts` | 719 | `MemberID: string;` field on a guarantor row — same rename needed. |
| `web/admin/src/api/client.ts` | 2957 | `guarantor_member_id: string;` on a guarantee shape — same rename. |
| `web/admin/src/components/MemberAccountsPanel.tsx` | 614 | `a.guardian_member_id` — confirm column was renamed to `guardian_counterparty_id` (junior account guardian). |
| `web/admin/src/components/MemberAccountsPanel.tsx` | 882 | Placeholder `paste from /members/<id>` for **Recipient member ID (UUID)** is fine, but the underlying transfer payload field name should be checked against the savings API. |

## G. Untracked migrations in the working tree

```
services/member/internal/db/migrations/0013_status_mirror_trigger.{up,down}.sql
services/member/internal/db/migrations/0014_drop_materialized_member_org.{up,down}.sql
```

If these have been applied to the dev DB but not committed (or vice versa) you get exactly the symptoms in section A. The fix prompts below include a “sync before doing anything else” step.

---

# Prompts to paste into Claude Code

Each prompt is self-contained. Run them in order — the first one stabilises the dev environment so every later prompt has a working baseline.

---

## Prompt 1 — Resync DB schema with working-tree code (do this first)

```
We're on branch phase-e-c-housekeeping. There are untracked migrations
services/member/internal/db/migrations/0013_status_mirror_trigger.up.sql
services/member/internal/db/migrations/0014_drop_materialized_member_org.up.sql
plus matching .down.sql files, and the working tree has uncommitted edits in
services/member/internal/{domain/application.go, store/application_store.go}
and web/admin/{api/client.ts, pages/Applications/ApplicationDetail.tsx} that
rename materialized_member_id/materialized_org_id -> materialized_counterparty_id.

The dev DB and the running binaries are out of sync. Five list endpoints
currently return 500:
  GET /api/v1/applications
  GET /api/v1/loan-applications
  GET /api/v1/loans
  GET /api/v1/share-accounts
  GET /api/v1/deposit-accounts

Please:
1. Read each untracked migration and the working-tree diff. Confirm the
   migrations and code together form a consistent "drop materialized_member_id
   + materialized_org_id, switch to materialized_counterparty_id" change.
2. Use `make migrate` (or `psql` if you must) to bring the dev DB to the
   latest version after applying 0013 + 0014.
3. Rebuild the member and savings services (`make build` or `go build`)
   and restart them — `make up` followed by `docker compose restart
   member savings` is the usual pattern in this repo; verify with the
   Makefile.
4. After restart, curl each of the five endpoints above with a valid
   bearer token (grab one from localStorage.nx.tokens.v1 in the
   tujenge tenant browser tab) and confirm they return 200 with
   well-formed JSON. Paste the bodies.
5. If any endpoint still 500s, tail the relevant service log
   (`docker compose logs --tail=200 member savings`) and report the
   actual SQL error. Do NOT mask it — we need the column/relation
   name that's failing.

Constraints:
- Don't `git add` or `git commit` anything. Leave the working tree as-is.
- Don't run destructive psql commands beyond what's in the migration files.
- If a migration looks unsafe (e.g. drops a column with live data and no
  backfill), stop and surface the concern in a comment instead of running it.
```

---

## Prompt 2 — Fix the `CounterpartyProfile` member-id mismatch

```
The page at web/admin/src/pages/CounterpartyProfile.tsx loads when the URL is
/members/<id> or /orgs/<id>, and calls getMember(id) / getOrg(id) in
web/admin/src/api/client.ts, which hit:
  GET /v1/members/{id}
  GET /v1/orgs/{id}

Those member-service handlers expect a members.id / org_members.id, NOT a
counterparties.id. Today, web/admin/src/pages/Members.tsx routes via
`c.legacy_target_id ?? c.id`, so it works for counterparties that have a
legacy bridge but 404s for any counterparty without one — the page renders
"Couldn't load counterparty". Repro: open
http://tujenge.nexussacco.local:5173/members/1bd7e20f-8729-4b0d-9e16-912027725e2b
(that's CP-2026-00011, "Phase D Test Member" — its legacy_target_id IS set,
so the fallback also masks the bug for known rows. To force it, navigate to
/members/<any counterparty.id you know>.)

Fix it by making /v1/members/{id} and /v1/orgs/{id} accept EITHER a legacy
id OR a counterparty.id. Concretely:

1. In services/member/internal/handler/member.go and org.go, find the Get
   handlers. Before hitting the legacy store, try resolving the path id
   as a counterparty.id via the existing ResolveCounterpartyID-style helper
   (see services/member/internal/store/counterparty_resolve.go for the
   pattern); if it resolves to a counterparty whose legacy_target_id is set,
   substitute that id and continue.
2. Add a unit test in services/member/internal/handler/ that covers both
   inputs (legacy id and counterparty id) and asserts the same response
   shape.
3. Don't change the response shape. Don't add a new route — just expand
   what the existing route accepts.

After the fix, retest by navigating to
/members/1bd7e20f-8729-4b0d-9e16-912027725e2b in the tujenge tab — the
profile must render without "Couldn't load counterparty" / "Couldn't load
member" error.

Also: while you're in CounterpartyProfile.tsx, change every user-facing
string from "counterparty" to "member":
- line 111: empty={<div className="empty">No such counterparty.</div>}
            -> No such member.
- line 112: errorTitle="Couldn't load counterparty"
            -> Couldn't load member
- line 115: We couldn't fetch this counterparty's profile.
            -> We couldn't fetch this member's profile.
- line 196: ariaLabel="Counterparty sections"
            -> Member sections
- line 655: Audit log entries for this counterparty.
            -> Audit log entries for this member.
DO NOT rename the component (CounterpartyProfile), the type names, or the
file. Internal identifiers stay as counterparty.
```

---

## Prompt 3 — Scrub user-facing “Counterparty” copy from the rest of the UI

```
Goal: anywhere the human reader sees the word "Counterparty" / "counterparties" /
"CP #" / "Counterparty ID" in the admin web app, rename to "Member" /
"Members" / "Member #" / "Member ID". Code identifiers (types, variables,
component names, file names, route module names) stay as counterparty.

Concrete substitutions (search-and-replace one at a time, NOT a blind
sed; some hits are code identifiers and must stay):

In web/admin/src/pages/Members.tsx:
- line 118: "Onboard counterparty"        -> "Onboard member"
- line 205: <th>CP # / Legacy</th>        -> <th>Member # / Legacy</th>
- column header rendered above the table — keep "ON REGISTER / ACTIVE /
  PENDING REVIEW / REJECTED" wording.

In web/admin/src/pages/Applications/ApplicationDetail.tsx:
- line 134: <Field label="Counterparty ID" ...>  -> label="Member ID"
- line 137: "Open counterparty →"                -> "Open member →"

Also check (grep the directory and decide case-by-case — if it's user-facing
text in a tsx file, it's almost certainly UI copy, not an identifier):
- web/admin/src/components/MemberAccountsPanel.tsx
- web/admin/src/components/MemberAccountsSummary.tsx
- web/admin/src/components/MemberLedgerPanel.tsx
- web/admin/src/components/MemberStatusCard.tsx
- web/admin/src/pages/CounterpartyProfile.tsx (covered in prompt 2 — skip
  the lines already handled, but check tab labels and section headings)

DO NOT change:
- Type names: Counterparty, CounterpartyKind, CounterpartyStatus,
  CounterpartyKYCState, CounterpartyView, INSTITUTIONAL_KINDS.
- Component names: CounterpartyProfile, CounterpartyShell.
- File names: CounterpartyProfile.tsx.
- JSON field names: counterparty_id, counterparty_number, cp_number,
  legacy_target_id, etc.
- API endpoint paths: /v1/counterparties, /counterparties/by-* ...
- React Router-style route prefixes in App.tsx: /members/, /orgs/ already
  correct, leave them alone.
- Code comments may say "counterparty" — that's fine.

After: rebuild the frontend (npm run dev hot-reloads already, but run
`npm run build` to confirm no TS errors) and walk these pages in the
tujenge tab:
  /members         — "Onboard member" button, "Member # / Legacy" column header
  /members/<id>    — "Couldn't load member" if it errors, "Audit log entries
                     for this member" on the Activity tab
  /applications/<approved app id>  — "Member ID" + "Open member →"

Paste a list of every file you changed.
```

---

## Prompt 4 — Fix the broken JOIN in `loan_guarantee_store.go`

```
services/savings/internal/store/loan_guarantee_store.go:147 has an
incorrect JOIN that returns zero rows in production:

    JOIN members          m ON m.id = a.counterparty_id

a.counterparty_id is a counterparties(id) value. members.id is a separate
key space. The right JOIN is:

    JOIN members m ON m.counterparty_id = a.counterparty_id

But that only matches individual borrowers. The unified register can hold
institutional borrowers too, and "loans I'm guaranteeing" should show
those as well.

Please:
1. Replace the JOIN with one to counterparties, NOT to members, so the
   borrower's display_name comes from counterparties.display_name and
   the borrower_no comes from counterparties.cp_number:

       JOIN counterparties cp ON cp.id = a.counterparty_id

   And in the SELECT, swap m.full_name for cp.display_name, and decide
   whether borrower_member_id should now expose a.counterparty_id (yes —
   rename the alias to borrower_counterparty_id; update the Go struct
   field accordingly).

2. Update the Go struct GuarantorshipRow (look for it in the same file
   or store/types) so the field is named BorrowerCounterpartyID, JSON-
   tagged borrower_counterparty_id. Bump matching consumer code in
   handler/loan_application.go's ListByGuarantor + GuaranteeRespond
   (uses BorrowerID / BorrowerName).

3. The handler argument is still called memberID — rename it to
   counterpartyID locally, but the URL path param can stay
   {counterparty_id} (it already is per routes.go).

4. Add a test in services/savings/internal/store/ that inserts a
   counterparty + loan_application + loan_guarantee for an INSTITUTIONAL
   counterparty (kind='chama' say) and asserts the row comes back —
   today it doesn't. Use the existing test scaffolding in
   collections_bridge_test.go as a template for tenant context setup.

After: hit the impacted endpoint
  GET /api/v1/loan-applications/by-guarantor/{counterparty_id}
and confirm an institutional guarantor's guarantorships now appear.

Constraints: stay inside services/savings/. The frontend changes for
this rename (guarantor_member_id -> guarantor_counterparty_id) are
handled separately in prompt 5.
```

---

## Prompt 5 — Frontend: drop stale `guarantor_member_id` / `MemberID` references

```
We just renamed guarantor_member_id -> guarantor_counterparty_id on the
savings side (prompt 4). The frontend still references the old name in
three places — all break at runtime when the API responses change shape:

1. web/admin/src/pages/Loans.tsx:715
     <a className="tbl-link" href={`/members/${g.guarantor_member_id}`}>
       {g.guarantor_member_id.slice(0, 8)}…
     </a>
   The href should still point at /members/<...> (URL contract is stable),
   but the field is now g.guarantor_counterparty_id. Note: the URL
   needs to be the LEGACY target id, not the counterparty id, otherwise
   we recreate the bug from prompt 2. Best path: change the API response
   to include both guarantor_counterparty_id AND a derived
   guarantor_legacy_target_id (members.id for individuals, org_members.id
   for institutions) for link rendering. OR plumb a per-row legacy_target_id
   through the savings handler by joining counterparties + members (LEFT)
   + org_members (LEFT). Pick the lower-risk path and explain in a comment
   in the changed Go file.

2. web/admin/src/api/client.ts:719  — type field `MemberID: string;`
   on what looks like a guarantor view. Rename to CounterpartyID.

3. web/admin/src/api/client.ts:2957 — JSON shape field
   `guarantor_member_id: string;`. Rename to `guarantor_counterparty_id:
   string;`.

After: re-run `npm run build` to confirm no TS errors. Walk /loans in
the tujenge tab and confirm the guarantor links on the loan detail still
resolve to a working member profile.

DO NOT touch URL strings of the form /members/${...} — those routes are
the user-facing contract and we want them preserved.
```

---

## Prompt 6 — Stop silently dropping institutional rows from savings/loan lists

```
Every list query in services/savings/internal/store/ does an INNER JOIN
to members:

    JOIN members m ON m.counterparty_id = X.counterparty_id

This drops any institutional row (chama, company, ngo, etc.) — they live
in org_members, not members. Confirmed paths:

- loan_store.go            (LoanStore.ListTx)
- loan_application_store.go (LoanApplicationStore.ListTx + the count query)
- deposit_store.go         (DepositStore list)
- share_store.go           (ShareStore list + Summary helpers)
- dividend_store.go        (3 separate JOINs)
- loan_reports_store.go    (~5 JOINs across the file)
- loan_collections_store.go
- member_statement_store.go
- provisioning_store.go

The fix: every such JOIN should target the unified `counterparties` table
instead, pulling display_name + cp_number from it:

    JOIN counterparties c ON c.id = X.counterparty_id

In the SELECT, replace m.full_name with c.display_name and m.member_no with
c.cp_number. Where the query needs the legacy member_no (M-* format), join
LEFT to members + org_members on counterparty_id to surface it as an
optional column.

For each store file:
1. Make the JOIN change.
2. Update the matching scan / dest list so the column count matches.
3. Update the Go struct fields (LoanListItem.MemberName,
   AppListItem.MemberName, etc.) — keep the field NAMES (MemberName,
   MemberNo) since they're user-facing in the API contract and the
   frontend already binds to them. Just change the source column.
4. Add or extend tests that prove an institutional loan/share/deposit
   row appears in the list output.

Constraints:
- Don't change API URL paths.
- Don't change response JSON field names (member_no / member_name / etc.)
  — even though they're now actually counterparty columns. Renaming them
  is a bigger task that breaks the frontend in 30+ places; we'll do it in
  a follow-up.
- Run `make test` to confirm the savings package still passes.

After: walk /loans, /shares, /deposits, /collections in the tujenge tab
and confirm institutional rows now appear when present. Also run the
arrears summary + provisioning to make sure they don't drift.
```

---

## Prompt 7 — Verify and finalise

```
Smoke-walk the admin app end-to-end, paste a punch list of anything still
broken. Logged-in user: wamaemikke@gmail.com on the tujenge tenant.

For each route below, open it in the browser, wait for it to settle, and
report (a) whether any 4xx/5xx response showed up in the network panel,
(b) whether any visible "an unexpected error occurred" / "Couldn't load"
banner is on the page, and (c) screenshot if anything is off. Use the
Claude in Chrome browser tools if available.

  /                       — tenant dashboard
  /members                — directory KPIs + register table
  /members/<cp.id>        — for both an individual and an institutional row
  /orgs/<org.id>          — confirm the /orgs/* path still routes to the
                            unified profile
  /applications           — onboarding queue, "an unexpected error" banner
                            should be gone
  /applications/new       — capture form
  /applications/<id>      — detail view, "Open member →" button
  /loans                  — applications + active loans + arrears summary
  /loans/<id>             — detail view, guarantor links resolve
  /loans/provisioning
  /shares                 — share register
  /deposits               — deposit accounts
  /collections            — cases queue
  /approvals              — cross-system queue
  /interest-runs          — last few runs
  /dividend-runs          — last few runs

Also visit the platform-admin app:
  http://platform.nexussacco.local:5173/
and confirm /api/v1/notifications/unread + /api/v1/notifications return 200
(currently 400 — the platform tenant context probably isn't being passed
to the notification service; out of scope for member rename but worth
flagging in the punch list).

If you find any other UI strings reading "Counterparty" or "CP #" that
should say "Member", fix them and list them in the report. Goal:
zero "counterparty"-as-noun in user-visible copy after this pass.
```

---

# Summary

The dev environment is in a half-migrated state: the frontend has been refactored to read counterparty-shaped responses, savings and member migrations have dropped the legacy `member_id` columns + the materialized linkage columns, but the running backend binaries and a handful of frontend strings/fields are still on the old contract. Prompt 1 stabilises the schema/binary mismatch; prompts 2–6 fix the user-visible damage; prompt 7 is a sign-off pass.
