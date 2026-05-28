# Move System Health to the platform admin + unblank the page

## What's wrong (verified in code)

Two distinct bugs, both rooted in the same architectural mistake: System Health was scoped to tenant when service health is platform-wide.

### Architectural mistake — wrong scope

Service health is a **platform** concern, not a tenant concern. Every nexusSacco service is a single process instance serving every tenant; whether savings is up doesn't depend on which tenant is asking. The aggregator's fan-out across `/healthz` endpoints answers the same question for every tenant, with no per-tenant data.

The current implementation places the page at `/operations/system-health` and gates it on `tenant:operations:view` — a tenant-scoped permission. Tenant admins see it; **platform admins, the actual primary audience, do not** because the AppShell nav (`web/admin/src/components/AppShell.tsx:115`) hides the link when `onPlatform === true`. That's the immediate user-facing symptom.

### Why the page goes blank

A platform admin who types `/operations/system-health` directly into the URL bar:

1. Hits `App.tsx:176` which routes to `OperationsSystemHealthPage` regardless of context.
2. The component renders. `hasPermission('tenant:operations:view')` returns `true` for platform admins (per `AuthContext.tsx:123` — `is_platform_admin === true` short-circuits the permission check).
3. The component fires `api.get('/v1/system-health')`.

Two things go wrong here, either of which produces a blank-looking page:

- The endpoint registration on identity (`services/identity/internal/handler/routes.go:105-108`) sits **inside the auth group but outside the `RequirePlatform` gate**, which means it's reachable on both contexts — so the API call goes through. BUT the aggregator's downstream fan-out (`services/identity/internal/handler/system_health.go`) reads `SYSTEM_HEALTH_TARGETS` and, depending on how that env var is set, may either succeed or silently return an empty `services: []` payload that the UI renders as blank cards.
- The component's initial loading state is `snapshot === null && !err`, which renders `<div className="empty">Loading…</div>`. If the fetch errors but `extractError` returns an empty string for some axios error shapes (auth 401 on platform-host because permissions look up against a tenant context that doesn't exist), the `err` state stays falsy and the loading message never goes away.

The fix is to **move both the page and the endpoint into the platform-only path**, where the context is unambiguous and the permission model is straightforward.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. Move the System Health feature from the tenant admin to the platform admin where it belongs (service health is platform-wide, not tenant-scoped). Fix the blank-page bug in the process. Keep a stripped-down read-only health indicator on the tenant side so a tenant admin can still tell "is the platform up right now" without seeing platform internals.
>
> **Scope**
>
> 1. Add a platform-scoped route + permission for the aggregator endpoint, leave the tenant-side endpoint as a small read-only summary.
> 2. Move the full System Health page into the platform admin context, gated behind `is_platform_admin` and the platform nav section.
> 3. Add a slim tenant-side "Platform status" card on the tenant dashboard (small badge: green / amber / red + last-checked time + link).
> 4. Diagnose and fix the blank-page render path so the typed-URL case never silently shows a loading-forever screen.
> 5. Regression tests + auth-matrix tests.
>
> **Files you will read first**
>
> - `services/identity/internal/handler/routes.go:90-129` — the auth group + the `RequirePlatform` group (line 124-129).
> - `services/identity/internal/handler/system_health.go` — the aggregator handler.
> - `services/identity/internal/middleware/tenant.go:110-119` — `RequirePlatform` middleware.
> - `services/identity/internal/middleware/auth.go:53-74` — `RequirePermission` (note the `IsPlatformAdmin` short-circuit at line 61).
> - `services/identity/internal/db/migrations/0023_system_health.up.sql` — the current permission seed.
> - `web/admin/src/App.tsx:65-67, 175-176` — the two routes registered (`/accounting/system-health` and `/operations/system-health`).
> - `web/admin/src/pages/Operations/SystemHealth.tsx` — the page component.
> - `web/admin/src/pages/Accounting/SystemHealth.tsx` — the **duplicate** that exists. Delete this in this PR; it's a copy that shouldn't have shipped.
> - `web/admin/src/components/AppShell.tsx:100-140` — nav groups + platform-section gating.
> - `web/admin/src/pages/PlatformDashboard.tsx` — confirm how existing platform pages are structured.
>
> ---
>
> ### Step 1 — Backend: move endpoint to platform-only + add slim tenant endpoint
>
> 1. In `services/identity/internal/handler/routes.go`, move the existing `r.With(middleware.RequirePermission("tenant:operations:view")).Get("/system-health", d.SystemHealth.Get)` **out of** the general auth group and **into** the `r.Use(middleware.RequirePlatform)` group at line 125. Path becomes `GET /v1/platform/system-health`. Permission gate stays on `RequirePermission` but with the new `platform:operations:view` permission (Step 2).
> 2. Add a second route for tenants: `GET /v1/platform-status` (yes, no `/v1/tenant-status` — the URL describes what the data is about, not who's asking). Gated on a standard authed-tenant check (no platform requirement, no special permission — any authenticated tenant user can read it). This route calls a new lightweight handler `SystemHealthHandler.GetForTenant(w, r)` that returns ONLY `{overall_status, checked_at, message}` — no service-level details, no worker heartbeats, no URLs. Just enough for a tenant admin to know "platform is degraded; ops is aware." Cache it the same 5s as the full version.
> 3. Migration `services/identity/internal/db/migrations/0030_platform_operations_view.up.sql`:
>    ```sql
>    -- platform:operations:view supersedes tenant:operations:view for the
>    -- full System Health page. tenant:operations:view stays in the
>    -- permission catalog (it gates other things) but no longer grants
>    -- access to /v1/platform/system-health.
>    INSERT INTO permissions (code, description, category)
>    VALUES ('platform:operations:view',
>            'View the platform System Health dashboard',
>            'operations')
>    ON CONFLICT (code) DO NOTHING;
>
>    -- Grant to platform_admin (the canonical platform-level role).
>    -- Tenants do NOT get this permission. Platform admins also have
>    -- the IsPlatformAdmin claim short-circuit so they would bypass
>    -- this anyway, but the explicit grant keeps the audit clean.
>    INSERT INTO role_permissions (role_id, permission_code)
>    SELECT id, 'platform:operations:view' FROM roles WHERE code = 'platform_admin'
>    ON CONFLICT DO NOTHING;
>    ```
>    Matching `down.sql`. Don't delete `tenant:operations:view`; it may be granting other things. Just stop using it for this route.
> 4. Update `SystemHealthHandler.Get` to keep the rich response shape. Add a new `GetForTenant` that strips to `{overall_status, checked_at, message}` where `message` is auto-derived: ok → "All systems operational"; degraded → "Some non-critical systems are degraded — operations team has visibility"; down → "An outage is in progress — operations team is engaged".
> 5. Verify the aggregator's `SYSTEM_HEALTH_TARGETS` env var is set in `.env.example` so a fresh `make all-up` produces a non-empty fan-out. If absent today, add it.
>
> ### Step 2 — Frontend: platform page + tenant card + delete the duplicate
>
> 1. **Delete** `web/admin/src/pages/Accounting/SystemHealth.tsx` and remove its import + route from `App.tsx`. It's an unused duplicate from an earlier prototype.
> 2. **Move** the page to `web/admin/src/pages/Platform/SystemHealth.tsx`. Rename the default export to `PlatformSystemHealth`. Update the route in `App.tsx` to `/platform/system-health` → `<PlatformSystemHealth />`. Delete `/operations/system-health` from `App.tsx`.
> 3. Update the page's data fetch to call `api.get('/v1/platform/system-health')` (the new platform-scoped path).
> 4. Update the page's permission check from `hasPermission('tenant:operations:view')` to `hasPermission('platform:operations:view')`. The `is_platform_admin` short-circuit in `AuthContext` still grants this for platform admins.
> 5. **Fix the loading state** so a typed URL never silently hangs:
>    - When `snapshot === null && !err`, render the loading skeleton for **at most 10 seconds**. After that, render an error state explaining "Couldn't fetch system health within 10s. Check the browser console + network tab. The aggregator endpoint is `GET /v1/platform/system-health`; if it's returning non-2xx, the body will tell you why."
>    - On any axios error, surface `e?.response?.status` and `e?.response?.data` in the alert text (not just `extractError(e)` which can return empty strings for unusual error shapes). 401/403 should explicitly read "You don't have permission to view System Health. This page requires platform admin access."
>    - Wrap the entire page body in an `<ErrorBoundary>` (add a minimal one in `web/admin/src/components/ErrorBoundary.tsx` if one doesn't exist) so a render crash shows a stack instead of a blank screen.
> 6. **Nav: add to the Platform section** in `AppShell.tsx:131-140`. The platform group becomes:
>    ```tsx
>    if (onPlatform && user?.is_platform_admin) {
>      groups.push({
>        section: 'Platform',
>        items: [
>          { href: '/', label: 'Tenants & credits', icon: 'building', show: true },
>          { href: '/platform/system-health', label: 'System health', icon: 'check', show: true },
>        ],
>      });
>    }
>    ```
>    Remove the tenant-side Operations group entry (line 113-117) since the full page is gone from the tenant context.
> 7. **Add tenant-side slim card** on `web/admin/src/pages/TenantDashboard.tsx`. New component `<PlatformStatusBadge />` that fetches `/v1/platform-status` on mount (no polling — once-per-load is enough), renders a small inline pill at the top of the dashboard: "Platform operational • last checked 4s ago" (green) / "Platform degraded — operations team is aware" (amber) / "Platform outage in progress" (red). No deep-link. No further detail. This is the tenant-side answer to "is the platform up right now."
>
> ### Step 3 — Defensive renderer + diagnostics
>
> 1. In the moved `PlatformSystemHealth` page, after the fetch lands, log a single `console.info` line summarising the snapshot (`Platform System Health: status=ok, services=12, workers=3, infrastructure=postgres,redis`). If the snapshot is suspicious (empty `services` array, missing `infrastructure` block, all workers `down`), additionally log `console.warn` with the raw payload. This is what an operator opens DevTools to see when the page looks wrong.
> 2. Render unconditional debug strip in the page footer (small `muted tiny`) showing: `Endpoint: /v1/platform/system-health · Last fetch: <ISO timestamp> · Status code: <http status>`. Removes "is the API even being called" from the troubleshooting decision tree.
> 3. If `snapshot.services` is empty but the HTTP call succeeded, surface a banner above the page: "The aggregator returned an empty service list. Check `SYSTEM_HEALTH_TARGETS` on the identity service." This is exactly the failure mode that produces the user's blank page today.
>
> ### Step 4 — Tests
>
> Go:
> - `services/identity/internal/handler/system_health_test.go` — assert:
>   - `GET /v1/platform/system-health` on tenant subdomain returns 404 (RequirePlatform blocks).
>   - `GET /v1/platform/system-health` on platform host with platform-admin JWT returns 200 + full payload.
>   - `GET /v1/platform/system-health` on platform host with tenant-staff JWT returns 403.
>   - `GET /v1/platform-status` on tenant subdomain with authed tenant JWT returns 200 + slim payload.
>   - `GET /v1/platform-status` on platform host returns 200 (works in both contexts).
>   - Aggregator with empty `SYSTEM_HEALTH_TARGETS` returns `{overall_status: "degraded", services: [], infrastructure: {…}}` rather than an empty body — the UI relies on the envelope.
>
> React:
> - `web/admin/src/pages/Platform/SystemHealth.test.tsx` — render the page with three mocked payloads (all-ok, one-degraded, one-down) + assert pill colours + assert the debug strip is visible + assert "empty services" banner shows when applicable.
> - `web/admin/src/pages/TenantDashboard.platformBadge.test.tsx` — assert the slim badge fetches `/v1/platform-status`, renders the three states, never reveals service-level data.
>
> ### Step 5 — Documentation
>
> Update `docs/operations/system-health-runbook.md` (or create it) with:
> - URL: platform admins go to `/platform/system-health`.
> - Permission required: `platform:operations:view` (or `is_platform_admin: true` short-circuit).
> - Three sample failure-mode walkthroughs (the runbook the page already embeds — duplicate the text into the doc so it can be linked from elsewhere).
> - Diagnostic checklist when the page is blank: open DevTools → Network tab → confirm `/v1/platform/system-health` returns 200 with a non-empty `services` array; if 401/403 you're not platform admin; if 200 with empty services, fix `SYSTEM_HEALTH_TARGETS`.
>
> ### Acceptance walkthrough
>
> 1. Log in as platform admin on platform host. Sidebar shows "Platform → System health" as the second item. Click → page renders with overall status banner + service cards grouped by role + workers + infrastructure + runbook button + debug strip at the bottom showing the endpoint + HTTP 200.
> 2. Stop the mpesa container. Within 10s the page refreshes; mpesa card flips to red, overall banner flips to red.
> 3. Log out, log in as a tenant admin on a tenant subdomain. Sidebar has no "System health" link. Try typing `/platform/system-health` directly — the router can either (a) 404 the route at the App.tsx switch when `!onPlatform` or (b) let the page mount and surface the 401/403 from the backend. Pick (a) — cleaner UX.
> 4. On the tenant dashboard, the slim "Platform operational" badge appears at the top. With the mpesa container still stopped, the badge re-renders on next dashboard load showing "Platform degraded".
> 5. Verify `GET /v1/platform-status` from curl as an authed tenant staff user returns the slim shape. Verify `GET /v1/platform/system-health` from the same user returns 403.
> 6. Diagnostic test: clear `SYSTEM_HEALTH_TARGETS`, restart identity, reload the platform System Health page. The page renders the "aggregator returned empty service list" banner with the fix instruction — not a blank page.
>
> ### Idempotency / safety
>
> - The tenant-side `/v1/platform-status` is intentionally identical in shape across all callers — no per-tenant data leaks.
> - The deleted `Accounting/SystemHealth.tsx` was unused; confirm via `grep -r "Accounting/SystemHealth"` returns no callers before deleting.
> - Migration 0030 is additive only; the down script removes the new permission and its grant. `tenant:operations:view` stays in the catalog because other features may still gate on it (search `tenant:operations:view` to confirm; if no other use, remove it in a follow-up).
> - The new route `/v1/platform-status` does NOT require any permission — it's available to any authenticated user. The data is intentionally non-sensitive.
> - `gofmt`, `go vet`, full `go test`, `pnpm test`, `pnpm build` all green.
>
> When you're done, paste the new sidebar screenshot (platform context + tenant dashboard with the badge), the network-tab capture showing the new endpoint URL, and the test output into the PR description.

---

## Why this shape

Service health is platform-wide, full stop. The earlier prompt put it under tenant because that's where everything else lives, but it was a category error: the data isn't tenant-scoped and the audience isn't tenant staff. Moving it to the platform context fixes both the architectural mistake and (incidentally) the blank-page bug, because the new code path runs in a context where the permissions and routing are clear.

The slim `/v1/platform-status` is the courtesy view for tenant admins. They don't need to see service URLs, worker heartbeats, or outbox lag — they need to know whether "things feel slow" is on their end or the platform's. One pill on the dashboard answers that without leaking infrastructure.

The defensive renderer changes (debug strip, error-boundary, 10s loading timeout, empty-services banner) are the structural fix for the blank-page class of bug: a page should never silently fail. Whether the fetch returns 401, the aggregator returns empty, or a render crashes, the user should see *what failed*, not a white screen.