# The GL pipeline isn't running — diagnose, then close the operational gap

## What's actually wrong (verified in code)

The recent PR's code is correct. The approval executor in `services/savings/internal/handler/pending_approvals.go::executePayloadTx` does call `postSharePurchaseToGLTx` for `ApprovalKindSharePurchase` (line 668), which calls `postingops.PostShareBuyTx`, which calls `posting.Client.PostTx`. That last call inserts a row into `posting_outbox` inside the same tx. **That part works.**

The bug is operational, not architectural. Three observations from the repo as it stands today:

1. **The accounting service is not in `docker-compose.yml`.** Only `postgres, redis, adminer, workflow, member, identity, mpesa` have service blocks. `services/accounting` doesn't even have a `Dockerfile` to be containerised.
2. **The posting-dispatcher is not in `docker-compose.yml` or `Makefile`.** `services/savings/cmd/posting-dispatcher/main.go` exists, but no `make` target starts it and no compose entry runs it.
3. **`posting.Client.PostTx` silently no-ops in dev.** In `services/savings/internal/posting/client.go:210-213`:
   ```go
   func (c *Client) PostTx(ctx context.Context, tx pgx.Tx, in PostInput) error {
       if c == nil || c.Disabled {
           return nil // dev / test — outbox stays empty
       }
   ```
   And `New()` at line 70: `Disabled: baseURL == ""`. The dev `.env` doesn't set `ACCOUNTING_SERVICE_URL`, so it falls back to the default `http://localhost:8086` (config.go:62). That URL is non-empty, so `Disabled` is `false`, so `PostTx` *does* write the outbox row.

So when a share purchase is approved in the user's current setup:

- ✅ `share_transactions` row is created
- ✅ `share_accounts.shares_held` is bumped
- ✅ `posting_outbox` row is inserted (because Disabled = false)
- ❌ No process drains the outbox — the posting-dispatcher isn't running
- ❌ Even if the dispatcher were running, it'd call `http://localhost:8086/internal/v1/post` and connection-refuse because the accounting service isn't running
- ❌ `journal_entries` stays empty → no Balance Sheet impact → no SASRA impact

This isn't a code bug; it's a "the GL pipeline has nowhere to land in dev" gap. Close it three ways: get the accounting service running, get the dispatcher running, and make the no-op pattern in `PostTx` impossible to mistake for success.

## Confirm before fixing — run these against the dev DB

```sql
-- 1. Are outbox rows piling up?
SELECT count(*), max(enqueued_at), min(enqueued_at)
  FROM posting_outbox
 WHERE dispatched_at IS NULL;

-- 2. What's the JE-vs-subledger gap right now?
SELECT count(*) AS share_txns,
       count(t.journal_entry_id) AS with_je
  FROM share_transactions t;

-- 3. Same for deposits.
SELECT count(*) AS dep_txns,
       count(t.journal_entry_id) AS with_je
  FROM deposit_transactions t;

-- 4. Outbox row payload for the most recent share purchase — confirms
--    the executor wired correctly.
SELECT id, payload->>'source_module', payload->>'source_ref',
       enqueued_at, dispatched_at, attempts, last_error
  FROM posting_outbox
 WHERE payload->>'source_module' = 'savings.shares.purchase'
 ORDER BY enqueued_at DESC LIMIT 5;
```

If query 1 returns a non-zero pending count and query 4 shows your recent share purchases with `dispatched_at IS NULL`, the executor wiring is fine — the dispatcher just isn't running. If query 4 returns zero rows for purchases you know you made, then `PostTx` is silently no-op-ing and the silent-no-op fix below is what closes the gap.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. The GL pipeline is wired in code but the operational plumbing to run it doesn't exist. After this PR a developer cleanly running `make up` ends up with: accounting service running, posting-dispatcher running, every approved money event landing on `journal_entries` within seconds. And the silent-no-op pattern in `posting.Client.PostTx` becomes impossible to mistake for success.
>
> **Scope**
>
> 1. Containerise the accounting service.
> 2. Add docker-compose blocks for `accounting` and `savings` and a sidecar `posting-dispatcher`.
> 3. Make `posting.Client.PostTx`'s no-op mode explicit and noisy.
> 4. Add a `/healthz` check on savings that surfaces outbox lag.
> 5. Add a single `make` target that boots the whole money pipeline end-to-end and a dev-mode SQL check that fails CI if the gap reopens.
>
> **Files you will read first**
>
> - `docker-compose.yml` — current service blocks (postgres, redis, adminer, workflow, member, identity, mpesa). Pattern to follow for new entries.
> - `services/identity/Dockerfile`, `services/member/Dockerfile`, `services/workflow/Dockerfile`, `services/mpesa/Dockerfile` — template for the new accounting Dockerfile.
> - `services/accounting/cmd/server/main.go` — confirm the server entry point + the env vars it reads (DATABASE_URL, ACCOUNTING_HTTP_ADDR, ACCOUNTING_INTERNAL_TOKEN, etc.).
> - `services/savings/cmd/server/main.go` — confirm the savings entry point + how it reads `ACCOUNTING_SERVICE_URL`.
> - `services/savings/cmd/posting-dispatcher/main.go` — confirm the dispatcher's env vars, poll interval, and shutdown signals.
> - `services/savings/internal/posting/client.go:60-72, 210-258` — the `Disabled` field and the silent no-op.
> - `Makefile` — current targets pattern.
> - `.env.example` and `.env` — env-var conventions.
>
> ---
>
> ### Step 1: Containerise the accounting service
>
> 1. New file `services/accounting/Dockerfile`. Mirror `services/identity/Dockerfile` (multi-stage golang:alpine → distroless or alpine final). The HTTP port is `:8086` (matches savings's default for `ACCOUNTING_SERVICE_URL`). Confirm by reading `services/accounting/internal/config/` for the actual env-var name (likely `ACCOUNTING_HTTP_ADDR`).
> 2. Add to `docker-compose.yml`:
>    ```yaml
>      accounting:
>        build:
>          context: ./services/accounting
>          dockerfile: Dockerfile
>        restart: unless-stopped
>        environment:
>          DATABASE_URL: postgres://${POSTGRES_USER:-nexus}:${POSTGRES_PASSWORD:-nexus_dev_password}@postgres:5432/${POSTGRES_DB:-nexus_sacco}?sslmode=disable
>          ACCOUNTING_HTTP_ADDR: :8086
>          ACCOUNTING_ENV: ${ACCOUNTING_ENV:-development}
>          ACCOUNTING_LOG_LEVEL: ${ACCOUNTING_LOG_LEVEL:-info}
>          ACCOUNTING_INTERNAL_TOKEN: ${ACCOUNTING_INTERNAL_TOKEN:-dev-internal-secret-do-not-use-in-production}
>          JWT_SECRET: ${JWT_SECRET}
>          JWT_ISSUER: ${JWT_ISSUER:-nexus-identity}
>          APP_DOMAIN: ${APP_DOMAIN:-nexussacco.local}
>        ports:
>          - "8086:8086"
>        depends_on:
>          postgres:
>            condition: service_healthy
>    ```
> 3. Add the same `ACCOUNTING_INTERNAL_TOKEN` env var to `.env` and `.env.example`. Document in the comment that this is the shared secret between savings (caller of `/internal/v1/post`) and accounting.
>
> ### Step 2: Containerise the savings service + the posting-dispatcher sidecar
>
> 1. New file `services/savings/Dockerfile`. Mirror `services/mpesa/Dockerfile` since both ship two binaries (server + dispatcher). The image must build **both** `./cmd/server` and `./cmd/posting-dispatcher` into the final layer so the same image can be the entry point for two compose services.
>    ```dockerfile
>    # build stage
>    FROM golang:1.22-alpine AS build
>    WORKDIR /src
>    COPY go.mod go.sum ./
>    RUN go mod download
>    COPY . .
>    RUN CGO_ENABLED=0 go build -o /out/savings-server ./cmd/server \
>     && CGO_ENABLED=0 go build -o /out/posting-dispatcher ./cmd/posting-dispatcher
>
>    # runtime
>    FROM alpine:3.20
>    RUN apk add --no-cache ca-certificates
>    COPY --from=build /out/savings-server /usr/local/bin/savings-server
>    COPY --from=build /out/posting-dispatcher /usr/local/bin/posting-dispatcher
>    EXPOSE 8085
>    ENTRYPOINT ["/usr/local/bin/savings-server"]
>    ```
> 2. Add to `docker-compose.yml`:
>    ```yaml
>      savings:
>        build:
>          context: ./services/savings
>          dockerfile: Dockerfile
>        restart: unless-stopped
>        environment:
>          DATABASE_URL: postgres://${POSTGRES_USER:-nexus}:${POSTGRES_PASSWORD:-nexus_dev_password}@postgres:5432/${POSTGRES_DB:-nexus_sacco}?sslmode=disable
>          SAVINGS_HTTP_ADDR: :8085
>          SAVINGS_ENV: ${SAVINGS_ENV:-development}
>          SAVINGS_LOG_LEVEL: ${SAVINGS_LOG_LEVEL:-info}
>          ACCOUNTING_SERVICE_URL: http://accounting:8086
>          ACCOUNTING_INTERNAL_TOKEN: ${ACCOUNTING_INTERNAL_TOKEN}
>          JWT_SECRET: ${JWT_SECRET}
>          JWT_ISSUER: ${JWT_ISSUER:-nexus-identity}
>          APP_DOMAIN: ${APP_DOMAIN:-nexussacco.local}
>        ports:
>          - "8085:8085"
>        depends_on:
>          postgres:
>            condition: service_healthy
>          accounting:
>            condition: service_started
>
>      posting-dispatcher:
>        build:
>          context: ./services/savings
>          dockerfile: Dockerfile
>        restart: unless-stopped
>        entrypoint: ["/usr/local/bin/posting-dispatcher"]
>        environment:
>          DATABASE_URL: postgres://${POSTGRES_USER:-nexus}:${POSTGRES_PASSWORD:-nexus_dev_password}@postgres:5432/${POSTGRES_DB:-nexus_sacco}?sslmode=disable
>          ACCOUNTING_SERVICE_URL: http://accounting:8086
>          ACCOUNTING_INTERNAL_TOKEN: ${ACCOUNTING_INTERNAL_TOKEN}
>          POSTING_DISPATCHER_POLL_MS: ${POSTING_DISPATCHER_POLL_MS:-2000}
>          POSTING_DISPATCHER_LOG_LEVEL: ${POSTING_DISPATCHER_LOG_LEVEL:-info}
>        depends_on:
>          postgres:
>            condition: service_healthy
>          accounting:
>            condition: service_started
>    ```
>    Both services build from the same `services/savings` image; the dispatcher's `entrypoint:` override picks the second binary. No code duplication.
> 3. Re-read `services/savings/cmd/posting-dispatcher/main.go` and confirm the env var names match (POSTING_DISPATCHER_POLL_MS may already be wired; if not, wire it or remove the line from compose).
>
> ### Step 3: Stop silent no-ops in `posting.Client.PostTx`
>
> The current `Disabled: baseURL == ""` pattern silently swallows GL posts when the URL is empty. That made sense pre-dispatcher; now it's a footgun. Replace with a **loud** posture:
>
> 1. In `services/savings/internal/posting/client.go`:
>    - Rename the `Disabled` field to `DryRun` and document it: "When DryRun is true, PostTx logs a WARNING per call and skips the outbox insert. Only set in tests via `WithDryRun(true)`; production / dev paths must never set this."
>    - Change `New(baseURL, internalToken, logger)`:
>      - If `baseURL == ""` and `os.Getenv("SAVINGS_ALLOW_NO_ACCOUNTING") != "true"`, return an `error` (not a Client). The savings server's `main.go` then refuses to start when accounting is unreachable. Loud, not silent.
>      - The `SAVINGS_ALLOW_NO_ACCOUNTING=true` escape is for unit tests only.
>    - `PostTx` no longer no-ops on `c.Disabled` — instead:
>      ```go
>      if c.DryRun {
>          c.Logger.Warn("posting.PostTx: DRY-RUN — outbox row skipped",
>              "source_module", in.SourceModule,
>              "source_ref", in.SourceRef,
>              "tenant_id", in.TenantID)
>          return nil
>      }
>      ```
>      Every dev session that's missing accounting now logs a WARNING per money event — impossible to overlook.
> 2. Update every test that constructed `posting.New("", "", log)` to either:
>    - Use a `httptest.Server` mocking the accounting endpoint (preferred), or
>    - Set `SAVINGS_ALLOW_NO_ACCOUNTING=true` in `TestMain` for that package, and assert dry-run is logged.
> 3. Update `services/savings/cmd/server/main.go` to fail-fast when `posting.New` errors. Log the URL it tried to reach. The error message must include the fix: "Set ACCOUNTING_SERVICE_URL to a reachable accounting service (default: http://localhost:8086) or SAVINGS_ALLOW_NO_ACCOUNTING=true to bypass (tests only)."
>
> ### Step 4: `/healthz` + outbox lag check
>
> 1. Add `services/savings/internal/handler/healthz.go`. Route `GET /healthz` returning `{ status: "ok" | "degraded", outbox_pending: N, outbox_oldest_age_seconds: M, accounting_reachable: bool }`.
>    ```go
>    func (h *HealthHandler) Healthz(w http.ResponseWriter, r *http.Request) {
>        var pending int
>        var oldest *time.Time
>        // RLS disabled here — health is platform-wide. Run as the
>        // unscoped pool (h.DB.Pool, not h.DB.WithTenantTx).
>        _ = h.DB.Pool.QueryRow(r.Context(), `
>            SELECT count(*), min(enqueued_at)
>              FROM posting_outbox
>             WHERE dispatched_at IS NULL`).Scan(&pending, &oldest)
>        oldestAge := 0
>        if oldest != nil {
>            oldestAge = int(time.Since(*oldest).Seconds())
>        }
>        // Degraded when oldest pending row is > 60s old — means the
>        // dispatcher isn't draining. The threshold is configurable via
>        // SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S (default 60).
>        threshold := envIntOr("SAVINGS_HEALTHZ_OUTBOX_LAG_THRESHOLD_S", 60)
>        status := "ok"
>        if oldestAge > threshold {
>            status = "degraded"
>        }
>        // Cheap ping to accounting — TCP-level, not the auth round-trip.
>        accountingOK := canDial(h.AccountingURL, 500*time.Millisecond)
>        httpx.OK(w, map[string]any{
>            "status": status,
>            "outbox_pending": pending,
>            "outbox_oldest_age_seconds": oldestAge,
>            "accounting_reachable": accountingOK,
>        })
>    }
>    ```
>    Same shape on `services/accounting`'s server — straight `{status:"ok"}` is fine there.
> 2. Wire `/healthz` into the routes (NOT behind auth — health checks must be anonymous). Add to compose:
>    ```yaml
>          healthcheck:
>            test: ["CMD", "wget", "-qO-", "http://localhost:8085/healthz"]
>            interval: 30s
>            timeout: 3s
>            retries: 3
>    ```
> 3. Surface the same data in the admin UI: new card on `/accounting/system-health` (read by Settings → Operations). Shows pending outbox count + oldest-row age + accounting-reachable. Refreshes every 10s. **This is the page Mike opens next time he suspects the pipeline is silent.**
>
> ### Step 5: `make all-up` + dev gap-check
>
> 1. Add `make all-up`:
>    ```makefile
>    .PHONY: all-up
>    all-up: ## Bring up the full money stack: identity + member + workflow + accounting + savings + posting-dispatcher + mpesa
>        $(COMPOSE) up -d postgres redis identity member workflow accounting savings posting-dispatcher mpesa
>        @echo
>        @echo "Waiting 5s for services to settle..."
>        @sleep 5
>        @$(MAKE) money-pipeline-check
>
>    .PHONY: money-pipeline-check
>    money-pipeline-check: ## Verify the GL pipeline is end-to-end alive
>        @echo "  • savings   :"; curl -sf http://localhost:8085/healthz | jq .
>        @echo "  • accounting:"; curl -sf http://localhost:8086/healthz | jq .
>        @echo "  • outbox     :"
>        @$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) $(POSTGRES_DB) -c \
>          "SELECT count(*) FILTER (WHERE dispatched_at IS NULL) AS pending, count(*) FILTER (WHERE dispatched_at IS NOT NULL) AS dispatched FROM posting_outbox;"
>    ```
> 2. Add an integration test under `services/savings/cmd/server/healthz_integration_test.go` that boots savings + accounting + the dispatcher (via docker-compose-test, or via in-process pool sharing), creates a share purchase via the API, polls `/healthz` until `outbox_pending=0`, and asserts a matching `journal_entries` row appeared. Run this test in CI on every PR. **This is the wall against the bug reopening.**
> 3. Add a `tools/postingcheck` analyzer rule that flags any new call to `posting.New` outside of `cmd/server/main.go` and `cmd/posting-dispatcher/main.go`. The Disabled-mode pattern was abused via the test path; the analyzer prevents that returning.
>
> ### Step 6: existing-outbox backfill (one-time)
>
> Before this PR the outbox has been silently filling up with rows that have never dispatched. Once the dispatcher is up, those will all flush at once and risk overwhelming a freshly-started accounting service.
>
> 1. Add `services/savings/cmd/posting-dispatcher/--catchup` flag. When set, the dispatcher processes the entire backlog in one pass (no poll interval, just iterate until `pending=0`) with a 5/sec rate limit so accounting doesn't fall over. Logs a structured summary at the end.
> 2. Document the catchup pass in the PR description: "first run on each existing dev DB needs `posting-dispatcher --catchup` to flush whatever has accumulated since the outbox-refactor PR".
>
> ### Acceptance walkthrough
>
> 1. Wipe the dev DB. Run `make up && make migrate && make seed && make all-up`. All services come up. `make money-pipeline-check` returns `outbox_pending=0`, accounting & savings both `status:"ok"`, accounting reachable.
> 2. Log in as a tenant admin. From a member's profile, buy 10 shares of par 1000 KES. The page returns "Pending approval".
> 3. As a checker, approve the share purchase. The page shows posted.
> 4. Within ~3 seconds:
>    - `psql -c "SELECT count(*) FROM posting_outbox WHERE dispatched_at IS NULL"` returns `0`.
>    - `psql -c "SELECT count(*) FROM journal_entries"` returns `1` (or one more than before).
>    - GET `/v1/accounting/journal-entries` lists the balanced JE: DR cash, CR Member Share Capital.
>    - GET `/v1/accounting/reports/balance-sheet` shows "Member Share Capital" up by 10,000.
>    - GET `/v1/accounting/reports/sasra-return` reflects the change.
> 5. Stop the `posting-dispatcher` container. Make another share purchase + approve.
>    - `outbox_pending` rises to `1`.
>    - Wait 70 seconds. Re-fetch `/healthz` — `status:"degraded"`, `outbox_oldest_age_seconds > 60`.
>    - The admin UI's System Health card goes red.
>    - Restart `posting-dispatcher`. Within 10 seconds, outbox drains, health returns to OK.
> 6. Run `cd services/savings && SAVINGS_ALLOW_NO_ACCOUNTING= ACCOUNTING_SERVICE_URL= go run ./cmd/server` — the server refuses to start, logs the actionable error message from Step 3.
> 7. Run the same with `SAVINGS_ALLOW_NO_ACCOUNTING=true` — server starts but every money event emits a WARN log. Set up a share purchase — the warning fires; no outbox row is written.
> 8. CI integration test green: share-purchase → JE within 5s.
>
> ### Idempotency / safety
>
> - The compose changes are additive: `docker compose up` continues to start identity/member/workflow if you target them explicitly. `make up` keeps its old behaviour (the existing target unchanged); `make all-up` is the new full-stack one.
> - The `Disabled` → `DryRun` rename and the no-empty-URL constructor change is a breaking API change for tests only. Provide a one-paragraph migration note in the PR description.
> - The catchup-flag run is idempotent — the accounting service's `(source_module, source_ref)` dedup means flushing the same outbox row twice is safe.
> - `gofmt`, `go vet`, full `go test` across all services. Run `make lint` (the existing `postingcheck` analyzer) — extend it per Step 5.3.
> - When you're done, paste into the PR description: the new compose blocks, the migration steps for existing dev DBs (`make all-up` then `--catchup`), and the SQL output from the acceptance walkthrough showing `outbox_pending=0` and a balanced JE.

---

## Why this shape

The bug Mike is seeing has nothing to do with the executor-wiring PR that just shipped — that code is correct. The bug is that the dev environment has never actually run the GL pipeline. Every share purchase, deposit, and loan repayment since the outbox refactor has been writing to `posting_outbox` and nobody has been draining it. The user assumes "no JE means broken code" because that's the visible symptom; the truth is "no JE because the dispatcher isn't running and the accounting service isn't running."

Closing the gap is three pieces: get the missing services running by default (steps 1–2), make the silent-no-op pattern impossible to mistake for success in future (step 3), and surface outbox lag the moment it accrues so this state can never be invisible again (step 4). The CI integration test + analyzer rule (steps 5.2, 5.3) are the wall against regression.

The catchup flag (step 6) is a small operational detail but worth shipping in the same PR — the first dev run after this lands will have potentially hundreds of pending outbox rows from past test data, and we want the dispatcher to handle them gracefully.