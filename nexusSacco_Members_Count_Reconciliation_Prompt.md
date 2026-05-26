# Members page numbers don't reconcile — fix the three-way drift

## Symptom (what Mike sees)

On `/members`, three different numbers disagree:

1. **Page sub-title** — "_N total · X individuals · Y organisations_".
2. **Top KPI strip** — "_On register · Active · Pending · Rejected_".
3. **Register table** — number of rows shown.

And the dashboard's "Members — status overview" (home page) reports a fourth number that disagrees with all of the above.

## Why they disagree (already verified in code)

The page consumes **two different backends** that each measure a different thing, plus a paginated list, plus a dashboard that mirrors one of the backends.

### Source A — `/v1/counterparties` (drives the sub-title + the register table)

`services/member/internal/handler/counterparty.go::List` (≈line 42) returns `{ counterparties, total, individuals, institutions }`. These are tallied over the **counterparties** table — every kind, every status — and the list is paginated (`limit: 100`, see `web/admin/src/pages/Members.tsx:90`). So the sub-title says "_250 total · 180 individuals · 70 organisations_" and the table shows 100 rows. Everyone gets counted, including `exited / deceased / rejected`.

### Source B — `member_status_counts(tenant_id)` (drives the KPI strip + the dashboard widget)

`services/member/internal/db/migrations/0006_member_status_counts.up.sql` defines the SQL function. It queries **only the `members` table**:

```sql
SELECT status::text AS s, count(*)::int AS n
  FROM members
 WHERE tenant_id = p_tenant_id
 GROUP BY status
```

`total_on_register = active + dormant + pending + suspended + blacklisted`; `exited / deceased / rejected` are excluded by design. **Organisations are excluded entirely** — `org_members` is never touched. The Members KPI strip (`Members.tsx::MemberRollCallKPIs`) and the dashboard panel (`TenantDashboard.tsx::MemberStatusPanel` at line 287) both consume this function via `/v1/members/status/counts` and `/v1/members/status/summary` respectively.

The migration's header comment freezes the intent: "SASRA roll-call is members-only." That was the correct stance when org members were a side feature; now that institutional counterparties are first-class (and the user has institutions on the books), the function is silently under-counting.

### Source C — visible row count

The table renders `rows.length` (≤100) against a `total` from Source A. When `total > 100`, the table count is just a pagination artefact — but the page has no "load more" / pager, so it looks like data is missing.

### Source D — the four numbers don't honour the active filter

Switching the `kind` chip to `individual` updates the **list**, but `getMemberStatusCounts()` is called with no parameters, so the KPI strip never changes. Same when picking a status chip — the list filters, the KPIs don't. So a user can be staring at "_5 shown_" beside a KPI card saying "_180 active_".

### Net effect

- **Sub-title** counts everyone (ind + org), every status.
- **KPI strip** counts individuals only, excludes `exited / deceased / rejected`.
- **Dashboard panel** = KPI strip (same backend), so the dashboard agrees with the KPI strip and disagrees with the sub-title and the register table.
- **Register row count** is whichever page the user is on, capped at 100.

Four numbers, three semantics, no way to tell them apart.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. The Members page (`/members`) has four numeric surfaces that disagree with each other and with the dashboard's "Members — status overview" widget. Fix the drift end-to-end: make every number on the page derive from one canonical query whose scope tracks the page's active filters, include institutional counterparties everywhere, and make the dashboard widget tell the same story.
>
> **Scope**
>
> 1. **Backend (Go, `services/member`)** — collapse to one canonical counts source that covers both kinds and accepts the same filter dimensions the UI uses.
> 2. **Frontend (`web/admin/src/pages/Members.tsx`, `TenantDashboard.tsx`)** — wire every number to that source; remove the divergent code paths.
> 3. **UX polish** — give the user a clear pager and an explicit explanation of which scope each number reflects.
> 4. **Regression tests** end-to-end so the gap can't reopen.
>
> **Background you must read first**
>
> - `services/member/internal/db/migrations/0006_member_status_counts.up.sql` — current canonical function (members-only).
> - `services/member/internal/store/status_store.go` lines ≈345–400 — `MemberStatusCounts` struct + `MemberStatusCountsTx`.
> - `services/member/internal/handler/status.go` ≈510 / ≈561 — `/v1/members/status/summary` and `/v1/members/status/counts` handlers.
> - `services/member/internal/handler/counterparty.go::List` ≈line 42 — list endpoint returning `total / individuals / institutions`.
> - `services/member/internal/store/counterparty_store.go` `ListTx` — confirm how the per-kind totals are computed (read the file); the new counts function should reuse the same join/predicate logic so the two sources can't drift again.
> - `web/admin/src/pages/Members.tsx` (full file — read it; the page already has a comment at line 135 that openly acknowledges this gap).
> - `web/admin/src/pages/TenantDashboard.tsx` lines 269–320 — dashboard `MemberStatusPanel`.
> - `web/admin/src/api/client.ts` lines ≈1707–1781 (counts types + fetchers) and ≈1826–1860 (list types + fetcher).
> - `services/member/internal/db/migrations/0007_counterparties.up.sql` — confirms `counterparties.kind` is the universal discriminator. Note that the `members` and `org_members` legacy tables both FK to `counterparties` 1:1; the `counterparties.status` mirror is kept in sync by the existing materialisation path. Use `counterparties` as the canonical row source for the new function — it covers both kinds in one place and respects RLS.
>
> **Migration: new canonical counts function**
>
> Add a new migration `services/member/internal/db/migrations/00NN_counterparty_status_counts.up.sql` (next sequential number; do not edit `0006_member_status_counts`). Define:
>
> ```sql
> CREATE OR REPLACE FUNCTION counterparty_status_counts(
>   p_tenant_id   uuid,
>   p_kind        text DEFAULT 'all',          -- 'all' | 'individual' | 'institutional'
>   p_status      text[] DEFAULT NULL,         -- NULL = no status filter
>   p_q           text DEFAULT NULL            -- NULL = no search filter
> )
> RETURNS TABLE (
>   active                  int,
>   dormant                 int,
>   pending                 int,
>   suspended               int,
>   blacklisted             int,
>   exited                  int,
>   deceased                int,
>   rejected                int,
>   total_on_register       int,   -- active + dormant + pending + suspended + blacklisted
>   total_active_servicing  int,   -- active + dormant
>   total_directory         int,   -- every counterparty regardless of status
>   individuals             int,   -- total_directory split by kind
>   institutions            int
> )
> LANGUAGE sql STABLE
> AS $$
>   WITH base AS (
>     SELECT c.id, c.kind, c.status::text AS s
>       FROM counterparties c
>      WHERE c.tenant_id = p_tenant_id
>        AND (p_kind = 'all'
>             OR (p_kind = 'individual'    AND c.kind  = 'individual')
>             OR (p_kind = 'institutional' AND c.kind <> 'individual'))
>        AND (p_status IS NULL OR c.status::text = ANY (p_status))
>        AND (p_q IS NULL OR p_q = ''
>             OR c.display_name ILIKE '%' || p_q || '%'
>             OR c.cp_number    ILIKE '%' || p_q || '%'
>             OR COALESCE(c.legacy_id, '') ILIKE '%' || p_q || '%')
>   ), counts AS (
>     SELECT s, count(*)::int AS n FROM base GROUP BY s
>   ), buckets AS (
>     SELECT
>       COALESCE((SELECT n FROM counts WHERE s = 'active'),      0) AS active,
>       COALESCE((SELECT n FROM counts WHERE s = 'dormant'),     0) AS dormant,
>       COALESCE((SELECT n FROM counts WHERE s = 'pending'),     0) AS pending,
>       COALESCE((SELECT n FROM counts WHERE s = 'suspended'),   0) AS suspended,
>       COALESCE((SELECT n FROM counts WHERE s = 'blacklisted'), 0) AS blacklisted,
>       COALESCE((SELECT n FROM counts WHERE s = 'exited'),      0) AS exited,
>       COALESCE((SELECT n FROM counts WHERE s = 'deceased'),    0) AS deceased,
>       COALESCE((SELECT n FROM counts WHERE s = 'rejected'),    0) AS rejected,
>       (SELECT count(*)::int FROM base) AS total_directory,
>       (SELECT count(*)::int FROM base WHERE kind  = 'individual') AS individuals,
>       (SELECT count(*)::int FROM base WHERE kind <> 'individual') AS institutions
>   )
>   SELECT
>     active, dormant, pending, suspended, blacklisted, exited, deceased, rejected,
>     (active + dormant + pending + suspended + blacklisted) AS total_on_register,
>     (active + dormant)                                     AS total_active_servicing,
>     total_directory, individuals, institutions
>     FROM buckets;
> $$;
>
> COMMENT ON FUNCTION counterparty_status_counts(uuid, text, text[], text) IS
>   'Canonical counterparty roll-call counts. Reads from counterparties to include both individual members and institutions. Supersedes member_status_counts(uuid) but kept additive so existing callers still compile. See migration 0006 + this file for bucket semantics.';
> ```
>
> Keep the legacy `member_status_counts(uuid)` in place — it still appears in tests + back-compat callers. Update its header comment to point at the new function as canonical.
>
> Mirror this in `services/member/internal/store/status_store.go`: add `CounterpartyStatusCountsTx(ctx, tx, tenantID, kind, statuses, q)` returning a new `CounterpartyStatusCounts` struct that supersedes `MemberStatusCounts`. Keep `MemberStatusCountsTx` but mark it `// Deprecated: prefer CounterpartyStatusCountsTx`. Do not delete it in this PR.
>
> **Backend handler changes**
>
> `services/member/internal/handler/status.go`:
> - Add a new handler `CountsV2` registered at `GET /v1/counterparties/status/counts`. It accepts the same query-param shape the list endpoint accepts (`kind`, `status`, `q`) — parse with the same helpers `splitCSV` / `domain.CounterpartyKind` already used in `counterparty.go`. Returns the new struct.
> - Update `Summary` (`/v1/members/status/summary`) to call `CounterpartyStatusCountsTx` internally with `kind='all', status=NULL, q=NULL` so the dashboard immediately picks up institutions. Keep the response shape backwards-compatible; add `individuals`, `institutions`, `total_directory` as optional fields the dashboard widget can read.
>
> Routes (`services/member/internal/handler/routes.go`):
> - Add `r.With(middleware.RequirePermission("members:view")).Get("/counterparties/status/counts", d.Status.CountsV2)`.
> - Keep `/members/status/counts` and `/members/status/summary` registered for back-compat; both now resolve through the new function.
>
> **Frontend wiring**
>
> `web/admin/src/api/client.ts`:
> - Add `export type CounterpartyStatusCounts = MemberStatusCounts & { total_directory: number; individuals: number; institutions: number }`.
> - Add `getCounterpartyStatusCounts(p: { kind?: 'all'|'individual'|'institutional'; status?: CounterpartyStatus[]; q?: string }): Promise<CounterpartyStatusCounts>`. Forward the same params the list endpoint takes; use repeated `?status=…` like `listCounterparties` already does.
> - Extend `MemberStatusSummary` with `individuals?: number`, `institutions?: number`, `total_directory?: number` (optional so older callers compile).
> - Mark `getMemberStatusCounts` as deprecated in a JSDoc; leave it functional.
>
> `web/admin/src/pages/Members.tsx`:
> - Replace `getMemberStatusCounts()` with `getCounterpartyStatusCounts({ kind: kindFilter, status: filter === 'all' ? undefined : [filter], q: q || undefined })`. Re-run on every change of `kindFilter`, `filter`, `q` (use a debounce for `q`).
> - Drop the separate `total / individuals / institutions` state derived from `listCounterparties` — read those off the counts response instead. The list response's `total` should equal `counts.total_directory` when filters match; assert this in dev mode (`if (import.meta.env.DEV && r.total !== counts.total_directory) console.warn(...)`).
> - Reshape the sub-title to match the active scope so it can never lie:
>   - When `kindFilter==='all'` & `filter==='all'`: "_{total_directory} total · {individuals} individuals · {institutions} organisations_".
>   - When a status chip is active: "_{matching status count} {status_label} · scope: {kind_label}_".
>   - When a kind chip is active: "_{individuals or institutions} in scope · {total_directory} total in directory_".
> - **Pagination**: replace the hard `limit: 100` with a proper pager. Add `offset` state, "Previous / Next" buttons, and a "Page N of M" label. The register card's `card-sub` reads "_Showing rows {offset+1}–{offset+rows.length} of {counts.total_directory}_". Removes the user's impression that data is missing.
> - **KPI strip** (`MemberRollCallKPIs`): rename the cards to be explicit about scope: "On register (in scope)", "Active (in scope)", "Pending review (in scope)", "Rejected (in scope)". On hover, show a tooltip explaining the bucket semantics from `0006_member_status_counts.up.sql` comments.
> - Add a small "_How these are counted_" disclosure link beside the KPI strip → expand a panel explaining `total_on_register` (excludes `exited / deceased / rejected`) vs `total_directory` (everyone). Source the copy from the SQL function's COMMENT.
>
> `web/admin/src/pages/TenantDashboard.tsx`:
> - Update `MemberStatusPanel` header to read "_Counterparties — status overview_" (or keep the "Members" label but add a sub-line "_includes organisations_"). The widget already binds to `MemberStatusSummary`; expose `individuals` + `institutions` next to the existing `total_on_register / total_active_servicing` so the dashboard tells the full story.
> - Add an "Open organisations register →" link beside the existing "Open register →" link that jumps to `/members?kind=institutional`.
> - The `STATUS_ORDER` chips should hyperlink to `/members?status=X&kind=all` (today they only carry `status=`). Verify the page picks `status` out of the URL — wire that on Members.tsx if it doesn't already.
>
> **Quick-fact wiring**
>
> Members.tsx already reads `kindFilter` from `?kind=…`. Add the same for `?status=…`: parse it once into `initialFilter`, default to `'all'`, push back into the URL alongside `kindFilter` on every change. That makes the dashboard chips deep-link correctly.
>
> **Tests**
>
> Go:
> - `services/member/internal/store/status_store_test.go` — fixtures with a mix of `individual` + `institutional` counterparties across every status; assert `counterparty_status_counts(tenant, 'all', NULL, NULL)` returns the expected totals; assert `kind='institutional'` returns only orgs; assert status filter restricts correctly; assert `q` filters by `display_name`, `cp_number`, `legacy_id`.
> - `services/member/internal/handler/status_handler_test.go` — happy-path GET on the new route with various filter combinations; assert response shape.
> - Add a multi-tenant test that confirms RLS still applies (call from tenant A, ensure tenant B's counterparties aren't counted).
>
> React (Vitest + RTL):
> - `web/admin/src/pages/Members.reconciliation.test.tsx` — render the page with a mocked client that returns: 250 directory, 180 individuals, 70 orgs, status distribution. Assert that:
>   - Sub-title = "250 total · 180 individuals · 70 organisations".
>   - KPI "On register (in scope)" matches the function output for `kind=all, status=null`.
>   - Switching kindFilter to "individual" updates KPIs, sub-title, and table count consistently — no number lags.
>   - Switching status chip to "pending" reduces sub-title to the pending count and the table to pending rows only.
>   - Pager Next/Prev moves the rows window and updates "_Showing rows X–Y of Z_".
> - `web/admin/src/pages/TenantDashboard.reconciliation.test.tsx` — assert the panel's `total` matches what `/v1/counterparties/status/counts?kind=all` returns for the same fixture.
>
> **Acceptance walkthrough**
>
> Set up a tenant with 250 counterparties: 180 individual + 70 institutional, status mix including a few `exited` and `rejected`.
>
> 1. Land on `/members`. Sub-title says "_250 total · 180 individuals · 70 organisations_". KPI strip shows the roll-call across both kinds — `total_on_register` excludes exited/deceased/rejected for both kinds. Register card-sub: "_Showing rows 1–100 of 250_". Pager visible.
> 2. Click "_institutional_" chip. Sub-title flips to "_70 in scope · 250 total in directory_". KPI cards switch to institutional-only counts. Register shows org rows; card-sub reads "_Showing rows 1–70 of 70_" and the pager hides.
> 3. Click "_pending_" status chip on top of the institutional scope. KPIs and sub-title both collapse to the pending-institutional count. Search "X" — counts and rows tighten together.
> 4. Open `/` (dashboard). The Members — status overview widget shows the same `total_on_register` you saw on the unfiltered Members page. Total servicing matches. Status chips deep-link to `/members?status=X&kind=all`.
> 5. Click the "Rejected" KPI on /members — sub-title says "_rejected · scope: all_" and the table shows exactly the rejected counterparties; org-kind rejected ones appear too.
> 6. SQL sanity: `SELECT * FROM counterparty_status_counts('{tenant}', 'all', NULL, NULL);` returns the same numbers the UI shows.
>
> **Idempotency / safety**
>
> - Migration is forward-additive (new function, comments on the old). `.down.sql` drops the new function only.
> - No table-shape changes.
> - Keep `/v1/members/status/counts` and `/v1/members/status/summary` for any external dashboard wiring.
> - RLS: `counterparties` already enforces tenant isolation; the new function still passes `p_tenant_id` for belt-and-suspenders.
> - Run `gofmt`, `go vet`, `go test ./services/member/...`, `pnpm test` (or `npm test`) before opening the PR.
> - When done, paste the diff stat and a screenshot of the Members page in three scopes (all / individual / institutional + a status filter) into the PR description, plus the SQL count output from the acceptance walkthrough.

---

## Why this shape

The drift is the symptom of two different definitions of "member": one rooted in the legacy `members` table, one in the unified `counterparties` table. Collapsing the counts to one parameterised function — and threading the page's active filters through to it — makes every number on the screen the answer to the same SQL question, scoped the same way the user is scoping the table. The pager and the "in scope" labels remove the remaining cognitive load: the user can always tell why a number is what it is. The dashboard widget moves to the same source, so the home page and the directory tell the same story.