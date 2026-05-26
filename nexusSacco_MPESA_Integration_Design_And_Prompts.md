# nexusSacco — M-PESA Integration: System Review, Feature Spec, and Phased Claude Code Prompts

## 1. Executive summary

nexusSacco today accepts M-PESA only as a **manual channel label** that a cashier or staff member picks at the Collection Desk. The system has no live link to Safaricom — there is no callback handler, no paybill registry, no automatic distribution of incoming money across the SACCO's product portfolio, and no outbound disbursement path. This document is the end-to-end design for closing that gap and a phased Claude Code prompt set for shipping it.

The end-state we are aiming at:

- A tenant can register **one or more M-PESA paybills** in nexusSacco, point Safaricom's confirmation / validation URLs at our service, and from that moment on every Lipa-na-M-PESA payment to that paybill is recorded automatically.
- Each paybill is **scoped to one or more product types** (Fees, Loan repayments, BOSA deposits, FOSA savings, Shares). A tenant can route, e.g., a "Loans" paybill and a separate "Savings" paybill at different Safaricom shortcodes.
- Incoming money is split per a **canonical waterfall**: fees & penalties → accrued interest → loan principal → BOSA → FOSA (or back to BOSA when there is no FOSA).
- Tickets posted through M-PESA **bypass approvals** because Safaricom's own confirmation API already certifies the money is real, and they post atomically into the GL via the existing posting outbox.
- The same paybill registry powers **B2C** outbound payments (loan disbursements, dividend payouts, refund of declined application fees). A SACCO can also maintain a dedicated payout paybill.
- The whole feature runs against **Safaricom Daraja sandbox** during development, with credentials/URLs structured so the production cut-over is a configuration change — not a refactor.

## 2. Current-state review (what we are building on)

I traced the relevant code paths so the prompts below plug into existing surfaces instead of rebuilding them.

### What is already in place

- **`mpesa` exists as a channel enum** in three domains: `services/savings/internal/domain/deposit.go:148` (`DepChannelMpesa`), `domain/receipt.go:57` (`RCMpesa`), `domain/share.go:60` (`ChannelMpesa`). The Collection Desk already accepts `channel='mpesa'` with a free-text reference, and the deposit / share / loan-repayment handlers already write `posting_outbox` rows that the dispatcher drains. We are adding a programmatic producer for those same rows.
- **BOSA / FOSA segmentation** is live. `services/savings/internal/db/migrations/0028_bosa_fosa_segment.up.sql` adds `deposit_products.segment` (`'bosa' | 'fosa'`). The waterfall's "BOSA before FOSA" rule keys off this column.
- **Loan repayment waterfall** is configurable per-tenant. `services/savings/internal/db/migrations/0005_lending.up.sql:460` adds `tenant_operations.loan_repayment_waterfall text NOT NULL DEFAULT 'penalty,interest,principal,fees'`. Loan accounts carry `principal_balance`, `interest_balance`, `penalty_balance`, and `fees_accrued / fees_paid / fees_balance` columns we can read for the M-PESA distribution.
- **Transactional outbox** for GL postings is shipped (`services/savings/internal/db/migrations/0032_posting_outbox.up.sql`). Every cash-touching handler now inserts its `posting_outbox` row inside the business tx; a background dispatcher (`services/savings/cmd/posting-dispatcher`) drains it. The M-PESA confirmation handler hooks into the same outbox so the GL post never disagrees with the deposit row.
- **Loan fees** carry their GL credit code per-product (`migration 0034_loan_product_fees_gl_code.up.sql`) — the prior finance-recommendations R1 fix means every fee posts to a real CoA account.
- **Counterparty directory view** (migration `0037_counterparty_directory`) gives one canonical join target for individuals + institutions, used by member lookups.
- **Notification service** lives at `services/notification`. It already has an `sms/` sender with a "Safaricom sandbox" provider hook, used today for OTP. The same env-var pattern (provider, sender id, secrets) is the template for the M-PESA Daraja credentials.
- **Per-tenant operational settings** live in `services/identity/internal/db/migrations/0007_tenant_settings.up.sql::tenant_operations`. A `mpesa_settings` JSONB column is the right home for non-secret toggles (auto-reconcile, post mode, retry policy). Secrets sit in a new credentials table behind an envelope-encryption boundary.

### What is missing

- No paybill registry — there is nowhere in the schema to record "this tenant owns shortcode 174379 mapped to BOSA + Loans" and there is no admin UI to register one.
- No Safaricom-facing HTTP handlers — there is no service exposing `/validation` and `/confirmation` URLs that Daraja can call. The M-PESA channel today is just a label written by a human.
- No distribution engine — even if we wrote a confirmation handler, the splitting of one M-PESA amount across fees → interest → principal → BOSA → FOSA does not exist anywhere. The only repayment "waterfall" today applies inside a single loan (penalty,interest,principal,fees), not across products.
- No reconciliation surface — we cannot today answer "did every Safaricom transaction find a home in the GL today?" There is no statement reconciler endpoint and no daily reconciliation report.
- No B2C path — there is no outbound payment client, no payout authorisation flow, no callback handler for B2C results.
- No member resolver for paybills — even with a registry, mapping a paybill `BillRefNumber` ("account number" the payer types) to a SACCO member is unbuilt. We need a deterministic resolver that handles the common typing-error cases.
- No audit/observability — every other money-touching path writes structured audit rows; this one needs the same hooks.

## 3. Feature surface (what we are building)

### 3.1 Tenant-facing concepts

- **Paybill account** — `{shortcode, account_label, provider='mpesa', purpose='c2b'|'b2c'|'both', scope=[fees,loans,bosa,fosa,shares], status, sandbox|production}`. A tenant can have any number of these. Each paybill carries its own Daraja consumer key/secret, passkey, and (for STK Push) initiator credentials. The Settings UI lets staff add, edit, deactivate, and rotate credentials. The Safaricom-side URLs to register on the Daraja portal are surfaced read-only in the same UI.
- **Distribution policy** — global default lives at tenant level (`fees → penalties → interest → principal → BOSA → FOSA`). Each paybill can override; each paybill's `scope` clips the waterfall ("loans-only" paybill stops after principal). A tenant can also override per-member (a saver who explicitly wants 100% to FOSA can have a flag) but the default has to work without per-member config.
- **C2B inbound** — Safaricom POSTs to `/v1/mpesa/c2b/{paybill_id}/confirmation`. We verify the source IP / shared secret, persist a raw `mpesa_inbound_events` row, kick off the distribution engine, and write a deterministic chain of receipts. Validation URL (`/validation`) is optional — turn it on only when the SACCO wants to refuse unknown member numbers.
- **B2C outbound** — at loan disbursement (or any payout flow) the workflow service queues a `mpesa_outbound_request` with `{paybill_id, msisdn, amount, source_ref}`. A dispatcher signs and posts to Daraja; result lands on `/v1/mpesa/b2c/{paybill_id}/result`. Success posts the cash leg DR/CR and stamps the loan disbursement.
- **Receipt — auto-posted, no approval** — the universal approval policy (from the prior "configurable approvals" PR) has a new `kind = 'mpesa_auto'` which is **always off**. The handler explicitly stamps these rows `posted_by = 'mpesa_system'` with a non-null `external_validation_ref = MpesaReceiptNumber`. Audit log records the chain of writes.
- **Reconciliation** — daily job pulls the Safaricom transaction statement (Daraja `Account Balance` + transaction statement APIs) and compares against `mpesa_inbound_events` + `mpesa_outbound_requests`. Mismatches surface in a new "M-PESA reconciliation" admin page.

### 3.2 Hierarchy / distribution waterfall (canonical)

For an inbound payment of amount `A` to member `M` on a paybill scoped to set `S`:

```
remaining = A

if 'fees' in S:
    pay outstanding fees (loan_product_fees balance + standalone fees) - oldest-first
if 'penalties' in S:
    pay outstanding penalty_balance across this member's loans - oldest-first
if 'loans' in S:
    pay accrued interest_balance across active loans - oldest, then highest-rate
    pay principal_balance across active loans - oldest first
if 'bosa' in S:
    credit member's BOSA deposit account up to min-shares + min-deposit target,
    or to all of it if the tenant policy is "BOSA-first uncapped"
if 'fosa' in S and remaining > 0:
    credit FOSA savings account
    if no FOSA account: fall back to BOSA
if remaining > 0 after every leg:
    write to unallocated_clearing GL account, raise an exception, ask staff
```

Each leg writes its own receipt line and its own posting_outbox row. Every line is atomic with the inbound event (one outer tx). The distribution decision is deterministic and replayable from `mpesa_inbound_events` for audit.

The 'penalty' bucket is a sub-component of 'loans' in the per-loan waterfall but we surface it separately at the cross-product level because the SACCO can choose to charge penalties to a non-loan member (e.g. dormant-account penalty) and may want a paybill that only collects penalties.

### 3.3 Member resolver

The `BillRefNumber` (account number the payer types in M-PESA) is matched in priority order:

1. **Exact match on `members.member_no`** (legacy `M-YYYY-NNNNN`).
2. **Exact match on `counterparties.cp_number`** (canonical `CP-YYYY-NNNNN`).
3. **Exact match on `loans.loan_no`** — money goes straight to that loan, bypassing the cross-product waterfall (the payer already named the loan).
4. **Exact match on `deposit_accounts.account_no`** — same logic; money goes to that one account.
5. **Phone-number fallback** — strip the leading `254` / `+`, match against `counterparties.contact->>'phone'`. Disabled by default per paybill; opt-in in the UI.
6. **Failure** → land the money in `mpesa_unallocated` (GL clearing account `1099`); raise a workflow notification for staff to reconcile manually.

The resolver runs inside the confirmation handler; its decision is persisted on `mpesa_inbound_events.resolved_member_id` for replay.

### 3.4 Data model (new objects)

```
mpesa_paybills
  id, tenant_id, label, shortcode, purpose (c2b|b2c|both),
  scope text[] (fees, penalties, loans, bosa, fosa, shares),
  default_distribution_policy_id, environment (sandbox|production),
  status (active|suspended|archived), created_at, …

mpesa_paybill_credentials
  paybill_id, kind (consumer|passkey|initiator|security_credential),
  ciphertext bytea, key_id text, rotated_at,
  -- Envelope-encrypted via a per-tenant data key

mpesa_distribution_policies
  id, tenant_id, name, waterfall jsonb, applies_default boolean

mpesa_inbound_events
  id, tenant_id, paybill_id, mpesa_receipt_number UNIQUE, msisdn,
  bill_ref_number, amount, transaction_time,
  raw_payload jsonb, signature_ok boolean,
  resolved_member_id, resolved_via text (member_no|cp_number|loan_no|account_no|msisdn|unallocated),
  distribution_run_id, posted_at, status (received|distributed|unallocated|failed),
  error_text, created_at

mpesa_distribution_runs
  id, inbound_event_id, started_at, completed_at, total_amount,
  splits jsonb -- [{leg: 'fees', amount, target_id, receipt_id}, …]

mpesa_outbound_requests
  id, tenant_id, paybill_id, source_module, source_ref UNIQUE,
  msisdn, amount, command_id, conversation_id, originator_conversation_id,
  status (queued|sent|completed|failed|reversed),
  daraja_response jsonb, mpesa_receipt_number, error_text, created_at, sent_at, completed_at

mpesa_reversal_events
  id, original_event_id, kind (c2b|b2c), reversed_amount,
  triggered_by (mpesa|staff), reversal_run_id, created_at
```

All tables RLS-scoped by `tenant_id`. Credentials table holds bytes only — never plaintext.

### 3.5 Service shape

A new dedicated microservice **`services/mpesa`** keeps the Safaricom-facing surface isolated. Putting it in `savings` would tangle webhook plumbing into a money-handling service that should stay focused.

```
services/mpesa/
  cmd/server               -- HTTP listener, Daraja webhooks
  cmd/distributor          -- background worker: processes mpesa_inbound_events
  cmd/b2c-dispatcher       -- background worker: drains mpesa_outbound_requests
  cmd/reconciler           -- daily job: Daraja statement vs our events
  internal/daraja          -- Daraja API client (sandbox + prod)
  internal/distribution    -- waterfall engine
  internal/handler         -- webhook handlers, admin paybill CRUD
  internal/store           -- pgx stores
  internal/domain          -- types
  internal/db/migrations
```

The distribution engine writes through the existing `posting_outbox` so it stays atomic with the inbound event tx. It calls into `savings` via the existing `/internal/v1` HTTP surface for deposit/loan/share posts — the same surface the manual handlers use. No duplication.

### 3.6 Security & secrets

- Daraja consumer key + secret + passkey + initiator password are stored in `mpesa_paybill_credentials` as ciphertext. Envelope encryption: a per-tenant data key (kept in `tenant_data_keys`, future) is wrapped by a master key (env var `MPESA_KMS_MASTER_KEY` in sandbox; KMS in prod).
- Inbound webhooks have IP allow-listing — Safaricom publishes the production IP ranges. Plus a shared-secret check using a header we register on the Daraja side.
- Outbound credentials never leave the service process. The b2c-dispatcher loads them on each call; we never log decrypted bytes.
- The Settings UI lets staff rotate any credential. Rotation writes a new ciphertext row and supersedes the old one; old rows are kept for audit.

### 3.7 Sandbox vs production

The Daraja client takes `Environment{Sandbox|Production}` and resolves URLs (`https://sandbox.safaricom.co.ke` vs `https://api.safaricom.co.ke`) and credentials accordingly. A `MPESA_FORCE_SANDBOX=true` env-flag overrides every paybill in non-prod deployments so we never accidentally hit prod from dev. Cert pinning is included from day one but the prod cert bundle ships in a follow-up commit.

### 3.8 Reversal posture

When Safaricom reverses (`/result` callback with `ReversalReceiptNumber`), we:

1. Mark `mpesa_inbound_events.status = 'reversed'`.
2. Walk `mpesa_distribution_runs.splits[]` and post offsetting receipts for each leg in reverse order (last in, first out).
3. If a leg's target row no longer exists (the loan was closed, the deposit withdrawn), the offset lands in `mpesa_unallocated` and raises a staff ticket.

Auto-reversal is the default. Per-tenant policy lets compliance flip it to "quarantine for review" — the original posts stay live, the reversal blocks on a workflow approval, and staff confirm the unwind manually. We default to auto for sandbox + dev; tenants must opt-in to quarantine in production.

### 3.9 Surfaces

- **Admin → Settings → M-PESA paybills** — list, add, edit, rotate credentials, view Daraja URLs, view recent inbound traffic, view reconciliation status per paybill.
- **Reports → M-PESA reconciliation** — daily statement diff with drill-down to the offending event.
- **Member 360 → Activity** — M-PESA receipts appear inline with manual receipts; the row carries an M-PESA chip.
- **Collection Desk → channel chip** — picking "M-PESA" in manual mode warns: "Inbound M-PESA money posts automatically via paybill. Use this only when reconciling a transaction the auto-poster missed."

## 4. Risks & open questions (call out before you ship)

- **Member identification** — typing a wrong account number is the dominant error in real Kenyan SACCOs. Empirically `M-2024-00123` works far less reliably than `00123`. Decision deferred: ship the resolver in §3.3 and add per-tenant rules for legacy short codes in a follow-up (do not block phase 2 on this).
- **Daraja rate limits** — sandbox is lax; production limits are real. The B2C dispatcher needs a per-paybill token bucket. Phase 4 builds it.
- **Concurrent reconciliation** — two distribution runs for the same `mpesa_receipt_number` must serialise. UNIQUE on `mpesa_receipt_number` + `SELECT … FOR UPDATE` on the row in the distribution worker enforces this.
- **STK Push** is out of scope for this round. The same paybill row can carry STK credentials later but phase 1–6 do not expose any push flow.
- **Off-hours posting** — Safaricom delivers callbacks 24/7. The dispatcher runs as a separate cmd so a paused web server doesn't lose webhooks; the webhook handler always 200s after persisting the raw row.

## 5. Phased Claude Code prompts

Each phase below is a self-contained Claude Code prompt that builds on the previous one. Ship them in order. Tests + acceptance walkthroughs are written into each prompt.

### Phase 1 — Service skeleton + Daraja sandbox client + secret storage

> You are working in the nexusSacco monorepo. Add a new microservice `services/mpesa` that hosts the Safaricom Daraja integration. **This phase is foundation only — no webhooks, no distribution, no UI.** Get the service compiling, talking to Daraja sandbox for an auth-token round-trip, and able to persist encrypted credentials.
>
> **Steps**
>
> 1. Scaffold `services/mpesa/` mirroring the layout of `services/notification`: `cmd/server`, `internal/handler`, `internal/store`, `internal/domain`, `internal/db/migrations`, `internal/daraja`, `internal/config`, `internal/middleware`. Reuse `shared/httpx`, `shared/middleware`, and the existing pgx-pool plumbing (read `services/notification/internal/config/config.go` as the template). HTTP listener on `:8084`.
> 2. Add a `Dockerfile` + a `mpesa` service block to `docker-compose.yml`. Env vars: `DATABASE_URL`, `MPESA_HTTP_ADDR=:8084`, `MPESA_ENV`, `MPESA_LOG_LEVEL`, `MPESA_DARAJA_BASE_URL` (defaults `https://sandbox.safaricom.co.ke`), `MPESA_FORCE_SANDBOX=true`, `MPESA_KMS_MASTER_KEY` (32 random bytes hex; ok to default for dev), `JWT_SECRET`.
> 3. Initial migration `services/mpesa/internal/db/migrations/0001_init.up.sql` defines:
>    - `mpesa_paybills` (see §3.4 above)
>    - `mpesa_paybill_credentials` (see §3.4 above)
>    - `mpesa_distribution_policies`
>    - The empty event/outbound/reversal tables (`mpesa_inbound_events`, `mpesa_distribution_runs`, `mpesa_outbound_requests`, `mpesa_reversal_events`) — schema-only, indexes, RLS, NO inserts yet.
>    Every table RLS-scoped by `tenant_id`. The credentials table has no `SELECT` grant for `nexus_app` except by id (handler reads); UPDATE only.
> 4. Build `internal/daraja` with:
>    - `Client` type holding `BaseURL`, `Environment(Sandbox|Production)`, `httpClient`.
>    - `Authenticate(ctx, consumerKey, consumerSecret) (Token, error)` — POSTs to `/oauth/v1/generate?grant_type=client_credentials`, parses `{access_token, expires_in}`. Caches in a per-paybill in-memory token cache keyed on `(paybill_id, key_id)` with TTL = `expires_in - 60s`. Use stdlib `http.Client` with `Timeout: 15s` and a `Transport` that pins the Safaricom root certs (in `internal/daraja/certs.go` — empty list in this PR; phase 6 fills it).
>    - Build the request signer for the C2B confirmation / B2C / reversal endpoints (just the function bodies; phases 2–5 actually call them). Use HMAC-SHA256 over `consumerKey + timestamp` per Daraja's spec. Unit test against the published sample vectors.
> 5. Crypto module `internal/crypto/envelope.go`:
>    - `Sealer.Encrypt(plaintext []byte) ([]byte, error)` and `Sealer.Decrypt(ciphertext []byte) ([]byte, error)`. AES-256-GCM with a key loaded from `MPESA_KMS_MASTER_KEY`. The key id is stamped into the ciphertext header so we can rotate keys later. Unit-tested round-trip + tampered-ciphertext rejection.
> 6. Two admin HTTP routes (no UI yet — just the API):
>    - `POST /v1/mpesa/paybills` — body `{label, shortcode, purpose, scope[], environment}`. Returns the created row.
>    - `POST /v1/mpesa/paybills/{id}/credentials` — body `{kind, plaintext}` — encrypts and stores. Plaintext never logged.
>    - `GET /v1/mpesa/paybills/{id}/test-auth` — pulls the stored consumer key/secret, calls `Authenticate`, returns `{ok, expires_at}`. Used by the Settings UI to confirm a paybill is wired correctly.
>    Permissions: `tenant:settings:edit` for write routes; `tenant:settings:view` for read.
> 7. Tests:
>    - `internal/daraja/client_test.go` — happy-path auth using `httptest.Server` mimicking Daraja sandbox.
>    - `internal/crypto/envelope_test.go` — encrypt/decrypt round-trip, tamper detection, wrong-key rejection.
>    - `internal/handler/paybill_test.go` — create + add credential + test-auth (with mocked Daraja).
>    - Integration test that runs the migration cleanly and exercises RLS across two tenants.
>
> **Acceptance**
> 1. `docker compose up mpesa` brings the service up clean.
> 2. `curl -X POST /v1/mpesa/paybills` (with a tenant JWT) creates a paybill row.
> 3. Adding credentials with bogus values lets `/test-auth` cleanly return `{ok: false, error: "..."}`. Real sandbox credentials (`MPESA_TEST_CONSUMER_KEY` / `MPESA_TEST_CONSUMER_SECRET` in `.env.test`) return `{ok: true}`.
> 4. SELECTing `mpesa_paybill_credentials.ciphertext` from psql returns bytes, not plaintext.
> 5. `go test ./services/mpesa/...` passes.

### Phase 2 — C2B inbound webhook + member resolver + raw event persistence

> Phase 2 catches money. Webhooks land, raw events persist, the member resolver runs — but **no GL posting yet**. The distribution engine is phase 3. The aim of this phase is "every paybill payment Safaricom sends us is stored verbatim and resolved to a member or marked unallocated; nothing is silently dropped."
>
> **Steps**
>
> 1. Migration `0002_inbound_events.up.sql` adds the per-event indexes you actually need: `(tenant_id, paybill_id, transaction_time DESC)`, `(tenant_id, mpesa_receipt_number)`, `(tenant_id, resolved_member_id) WHERE resolved_member_id IS NOT NULL`. `mpesa_receipt_number` is already UNIQUE from phase 1.
> 2. Two public routes (NO auth middleware — they are Safaricom-facing):
>    - `POST /v1/mpesa/c2b/{paybill_id}/validation` — Safaricom asks "is this account number known?" Daraja expects `{ResultCode: 0, ResultDesc: "Accepted"}` for accept and a non-zero for reject. By default we accept (`ResultCode: 0`); a tenant opt-in to "strict validation" (column `mpesa_paybills.strict_validation boolean default false`) makes us return `C2B00012` when the resolver fails to find a member.
>    - `POST /v1/mpesa/c2b/{paybill_id}/confirmation` — Safaricom hands us the finished transaction. Persist the raw body verbatim into `mpesa_inbound_events.raw_payload`. **Always 200 on success of persistence**, even if downstream resolution fails — Safaricom must not retry into a busy loop.
> 3. `internal/handler/webhook.go`:
>    - Wraps both handlers with an IP allow-list middleware. Allow list: env `MPESA_TRUSTED_IPS` (CSV); in sandbox the list is empty (allow all) with a WARN log; in production a non-empty list is mandatory (refuse to start otherwise).
>    - Verify a shared-secret header `X-NS-Paybill-Token` against `mpesa_paybills.webhook_token` (new column, generated on paybill create). The token is what staff plug into Daraja portal's URL with `?token=...`. Mismatched token → 401, no body persisted.
>    - Idempotency: `INSERT ... ON CONFLICT (mpesa_receipt_number) DO NOTHING` — Safaricom retries are absorbed.
> 4. `internal/distribution/resolver.go` — the member resolver (§3.3 above). Pure function, table-driven tests with all six branches.
> 5. After persist + resolve, the handler writes:
>    - `mpesa_inbound_events.resolved_member_id`, `resolved_via`, `status = 'received'` (NOT yet distributed).
>    - An audit row `mpesa.inbound_received` (use the existing audit table; key off the new `mpesa_inbound_events.id`).
>    - A workflow task `mpesa_unallocated` if the resolver landed there.
> 6. A staff-facing **read** route `GET /v1/mpesa/c2b/events` with paging + filter by paybill, msisdn, bill_ref_number, status, date range. This is what powers the "recent inbound traffic" panel in the Settings UI later.
> 7. Tests:
>    - Webhook round-trip with a fixed sample Safaricom payload (capture one from sandbox into a `testdata/` fixture).
>    - Idempotency: replaying the same payload writes one row and returns 200 both times.
>    - Resolver: each of the six branches has its own table-driven test.
>    - Strict validation enabled → unknown account returns the `C2B00012` body.
>    - Cross-tenant isolation: a paybill_id from tenant A cannot be hit with a token from tenant B.
>
> **Acceptance**
> 1. Register a sandbox paybill on the Daraja portal pointing at `https://<tunnel>/v1/mpesa/c2b/{paybill_id}/{validation,confirmation}?token=<webhook_token>`. Send a test C2B transaction from the simulator.
> 2. The transaction lands in `mpesa_inbound_events` with `status='received'`, `resolved_via='member_no'` (or whatever matched), and `resolved_member_id` populated.
> 3. `GET /v1/mpesa/c2b/events` shows it in the list.
> 4. No GL posting has happened — `journal_entries` is untouched. That is by design for this phase.

### Phase 3 — Distribution engine + auto-posting (the core feature)

> Phase 3 takes a resolved `mpesa_inbound_events` row, runs the waterfall, writes deposit/loan/fee receipts atomically, and queues GL posts via the existing `posting_outbox`. **No manual approvals.** The handler stamps `external_validation_ref = MpesaReceiptNumber` to mark the receipt as Safaricom-certified.
>
> **Steps**
>
> 1. New background worker `services/mpesa/cmd/distributor`:
>    - Polls `mpesa_inbound_events WHERE status='received' ORDER BY transaction_time` with `SELECT … FOR UPDATE SKIP LOCKED` (concurrency-safe, one row per worker per tx).
>    - Per row, opens a `WithTenantTx`, calls `distribution.Run(ctx, tx, event)`, commits.
>    - On success: marks `status='distributed'`, sets `distribution_run_id`, `posted_at`.
>    - On exception: increments `attempts`, sets `error_text`, leaves `status='received'`. Hard-fails after 6 attempts; an alert workflow task is created.
> 2. `internal/distribution/engine.go::Run`:
>    - Compute the paybill's effective scope (intersection of paybill.scope and the policy waterfall).
>    - If `resolved_via = loan_no` or `account_no`, skip the waterfall and post directly to that target.
>    - Otherwise iterate the waterfall legs. For each leg, query the outstanding balance from the appropriate store (read functions only — no writes from the engine; the engine produces a plan, then a single planner-applier writes everything in one tx).
>    - Build a `Plan{splits: []Split{leg, amount, target_id, narration}}`.
>    - Apply the plan: for each split, call into the **existing** savings handlers' tx-aware repositories (do NOT re-implement deposit/loan-repayment posting). Specifically:
>      - Fees → use the fee-collection executor used by the Collection Desk's fee-line (`services/savings/internal/handler/collection_desk.go::postFeeLineTx`). Extract a tx-level helper if needed so it can be called from the distributor.
>      - Penalty / interest / principal → call into `services/savings/internal/handler/loan_repayment.go` repository (extract a tx-level `RepayLoanTx` that takes precise components, bypassing the per-loan waterfall — we already chose the split).
>      - BOSA / FOSA → call `services/savings/internal/handler/deposit.go::depositToAccountTx`.
>    - Every split inserts a `posting_outbox` row inside the same tx. The existing `posting-dispatcher` drains them as usual.
>    - Persist `mpesa_distribution_runs.splits` jsonb for audit + replay.
> 3. **No approvals**. Add `kind='mpesa_auto'` to the approval-toggles enum, force it to `enabled=false`, and document in the migration header that this kind cannot be enabled. The receipt rows have `external_validation_ref` set; the universal-approval guard reads that field and skips the approval queue when it is non-null.
> 4. Audit:
>    - `mpesa.distribution_run.started` on Run entry
>    - one `mpesa.distribution_run.split` per split (with leg + amount + target)
>    - `mpesa.distribution_run.completed` on commit
> 5. Tests:
>    - `engine_test.go` — table-driven across the 14 scope/leg combinations: full waterfall, loans-only, BOSA-only, FOSA-only, fees-only, etc. Each test sets up a member with predictable balances and asserts the plan.
>    - Acceptance: a fixture tenant with one member who owes KES 200 in fees, KES 500 interest, KES 2000 principal, KES 0 BOSA target → KES 4000 inbound → expect 200 fees, 500 interest, 2000 principal, 1300 BOSA, 0 FOSA. Receipts written, GL outbox rows queued, audit chain complete.
>    - Replay: re-running distribution on a `status='distributed'` row is a no-op (idempotency on `mpesa_receipt_number`).
>    - FOSA-absent fallback: the same member with no FOSA product gets 100% of the leftover into BOSA.
>
> **Acceptance**
> 1. Run the phase-2 sandbox webhook end-to-end. The distributor picks up the event within 5s, distributes correctly, the receipt appears on the member's profile under Fees/Loans/Deposits as appropriate, and the GL Balance Sheet reflects the cash leg.
> 2. The Collection Desk does NOT show this transaction in its approval inbox — the universal-approval guard correctly skips it.
> 3. `SELECT splits FROM mpesa_distribution_runs WHERE id=...` returns a complete, structured audit trail.

### Phase 4 — B2C outbound + loan-disbursement integration

> Phase 4 sends money out. Loan disbursements (and future dividend payouts / refund flows) queue an `mpesa_outbound_requests` row; a dispatcher signs and posts to Daraja; the Result URL callback closes the loop.
>
> **Steps**
>
> 1. Migration `0003_outbound_indexes.up.sql` adds `(tenant_id, status, created_at)` index for the dispatcher poll, plus `(tenant_id, source_ref) UNIQUE`.
> 2. `internal/handler/b2c.go`:
>    - `POST /v1/mpesa/b2c/requests` — internal-only route (require `X-Internal-Token`, same pattern as the existing `/internal/v1/post` on accounting). Body: `{paybill_id, msisdn, amount, command_id, source_module, source_ref, remarks}`. Idempotent on `source_ref`.
>    - `POST /v1/mpesa/b2c/{paybill_id}/result` — public Daraja callback. Validates the shared-secret token. Persists raw result, sets `mpesa_outbound_requests.status` to `completed` / `failed`, populates `mpesa_receipt_number`.
>    - `POST /v1/mpesa/b2c/{paybill_id}/timeout` — public Daraja timeout callback. Marks the request `failed`, sets a retry timer.
> 3. `cmd/b2c-dispatcher`:
>    - Polls `mpesa_outbound_requests WHERE status='queued'` with FOR UPDATE SKIP LOCKED.
>    - Loads the paybill's initiator credentials, signs with `internal/daraja/security_credential.go` (RSA-encrypts initiator password with Safaricom public cert — sandbox cert ships in `internal/daraja/certs/sandbox.pem`).
>    - Calls `POST /mpesa/b2c/v1/paymentrequest` on Daraja. Persists the immediate response (conversation_id, originator_conversation_id) and marks `status='sent'`.
>    - Token-bucket rate limiter per paybill (default 30 req/min sandbox; configurable).
> 4. **Loan disbursement integration**:
>    - Read `services/savings/internal/handler/loan.go::Disburse`. Add an optional `disbursement_channel` field; when `=mpesa`, the handler:
>      1. Resolves the member's M-PESA MSISDN from `counterparties.contact->>'phone'`.
>      2. Picks the tenant's default B2C paybill (`mpesa_paybills WHERE purpose IN ('b2c','both') AND status='active' ORDER BY is_default DESC LIMIT 1` — add `is_default` column).
>      3. POSTs to `/v1/mpesa/b2c/requests` from inside the disbursement tx (writes the outbound row in the same DB tx via direct insert — no HTTP for this hop, the schema is shared).
>      4. The loan disbursement transaction is held in `pending_disbursement` until the result callback flips it to `disbursed`. Existing manual disbursement path stays the default.
> 5. **B2C → GL**: on `result` callback success, the handler queues two outbox rows:
>    - DR `Loans Receivable / member loan account` for the principal (already done by the loan-disburse path)
>    - CR `Cash at M-PESA paybill` (new GL code `1015 — M-PESA Paybill Cash`, one per paybill via subaccount labels; seed in migration).
> 6. Reversal: if Safaricom sends a reversal for a B2C, mark the outbound `status='reversed'` and flip the loan back to `pending_disbursement`. A staff workflow task asks them to retry or cancel.
> 7. Tests:
>    - Dispatcher happy path (mocked Daraja).
>    - Result callback success → outbound completed + loan disbursement posted.
>    - Result callback failure → outbound failed + loan rolled back.
>    - Idempotency on `source_ref`.
>    - Rate limiter: 31st call within a minute blocks; 32nd after the bucket refills succeeds.
>    - Loan disbursement test that exercises the manual path AND the M-PESA path with the same fixture; assert both produce identical GL state.
>
> **Acceptance**
> 1. Disburse a loan with `disbursement_channel='mpesa'`. Loan is `pending_disbursement` until the sandbox simulator confirms; then it flips to `disbursed`, the member's loan account shows the principal, and the GL shows a balanced CR-cash / DR-loan entry.
> 2. Reverse the disbursement on the simulator. Loan flips back to `pending_disbursement`, the outbound row is `reversed`, a workflow task is queued.

### Phase 5 — Admin UI (Settings → M-PESA paybills, Reports → M-PESA reconciliation)

> Phase 5 is the staff-facing surface. Everything until now has been API-only; staff can now register paybills, rotate credentials, view live traffic, and reconcile.
>
> **Steps**
>
> 1. New page `web/admin/src/pages/Settings/MpesaPaybills.tsx` mounted at `/settings/mpesa`. Lists all paybills for the tenant with:
>    - Shortcode, label, purpose (chip: C2B / B2C / Both), scope chips (Fees / Loans / BOSA / FOSA / Shares), environment chip (Sandbox / Production), status.
>    - "Test auth" button → calls `/v1/mpesa/paybills/{id}/test-auth`, shows green check or red error.
>    - "Rotate credentials" button → modal to update consumer key/secret/passkey/initiator with masked fields.
>    - Daraja URLs section (read-only, copy-to-clipboard): each paybill shows the exact validation/confirmation/result URLs the staff member registers on the Daraja portal, complete with the embedded `webhook_token`.
>    - Recent traffic panel: last 50 `mpesa_inbound_events` for this paybill, with status badge + drill-down to the distribution run + receipt.
> 2. Add to the main nav under Settings.
> 3. **B2C default paybill picker** in the loan disbursement modal — when staff start a disbursement they can choose Bank, Cheque, Cash, or M-PESA. M-PESA only enabled when the tenant has at least one `purpose IN ('b2c','both')` paybill. The picker shows the paybill label + last-4 of shortcode.
> 4. **Member 360 → Activity** — M-PESA receipts get an M-PESA chip. Tapping it opens a side-panel with the distribution split, the Safaricom receipt number, and a link to the raw event.
> 5. New page `web/admin/src/pages/Accounting/MpesaReconciliation.tsx` at `/accounting/mpesa-reconciliation`:
>    - Date-range picker (defaults to today).
>    - Three KPIs: "Inbound received", "Inbound distributed", "Unallocated".
>    - Table of `mpesa_inbound_events` with status, member, amount, distribution split, GL state.
>    - "Re-run distribution" action on rows in `failed` status (admin-only).
>    - "Statement diff" panel (read-only for this phase; phase 6 wires the real Daraja statement pull).
> 6. Tests (Vitest + RTL):
>    - Paybills page renders + Add modal validates + Rotate credentials sends ciphertext-bound payload.
>    - Reconciliation page filters by status.
>    - Permission gating: a non-`tenant:settings:edit` user sees the list read-only.
>
> **Acceptance**
> 1. From the UI alone, a tenant admin can add a sandbox paybill, paste in Daraja credentials, copy the confirmation URL into the Daraja portal, send a test transaction, see it appear in "Recent traffic", drill in to see the distribution, and confirm the member's profile reflects the new receipt.
> 2. From the disbursement modal, an officer can pick "M-PESA" and disburse to a member; the disbursement appears under "B2C" with a status that updates live as the result callback lands.

### Phase 6 — Reconciliation worker, production hardening, ops

> Phase 6 closes the prod-readiness gap. Daily statement pulls, cert pinning, on-call observability, runbook.
>
> **Steps**
>
> 1. `cmd/reconciler`:
>    - Daily job (cron-style; runs at 02:00 tenant-local via the existing scheduled-tasks plumbing).
>    - For each tenant + paybill, calls Daraja's `Account Balance` and statement endpoints, persists into `mpesa_statement_pulls`, diffs against our `mpesa_inbound_events` and `mpesa_outbound_requests`.
>    - Writes diffs into `mpesa_reconciliation_diffs` and raises a workflow task per diff.
> 2. Cert pinning: ship Safaricom production CA bundle in `internal/daraja/certs/production.pem`. The `Client` enforces pinning unless `MPESA_FORCE_SANDBOX=true`.
> 3. Observability:
>    - Structured logs with `paybill_id`, `mpesa_receipt_number`, `tenant_id` on every line.
>    - Prometheus-style metrics endpoint at `/metrics`: `mpesa_inbound_total{status}`, `mpesa_outbound_total{status}`, `mpesa_distribution_duration_seconds`, `mpesa_unallocated_total`.
>    - A `/healthz` and `/readyz` that fail when `posting-dispatcher` lag exceeds 60s.
> 4. Runbook: `docs/mpesa/runbook.md` covering:
>    - Go-live checklist for a new tenant
>    - How to rotate credentials
>    - How to manually reconcile an unallocated payment
>    - How to handle a reversal that landed on a closed loan
>    - Backfill procedure when the distributor is down for a window
> 5. Soak test: replay 1000 sandbox transactions end-to-end and assert reconciliation diff = 0.
> 6. Permission catalog: add `mpesa:paybill:manage`, `mpesa:reconcile:run`, `mpesa:credentials:rotate` distinct from `tenant:settings:edit` so customer success can be granted reconcile-only access without leaking credentials.
>
> **Acceptance**
> 1. A 24-hour sandbox soak produces zero unreconciled events.
> 2. `MPESA_FORCE_SANDBOX=true` rejects any prod URL the operator might fat-finger into config.
> 3. `/metrics` reports useful counters during the soak.
> 4. On-call doc walks through the four named scenarios with concrete SQL + UI steps.

## 6. Sequencing notes

- Phase 1 + 2 can ship behind a feature flag (`tenant.feature_flags.mpesa_paybills = false` by default). They are safe to merge without changing user-visible behaviour.
- Phase 3 is the bet — once it ships, real money flows. Treat the rollout as a percentage rollout per tenant via the same feature flag.
- Phase 4 unlocks the auto-loans feature (the user's stated motivation for B2C).
- Phase 5 is required for any non-engineer to adopt the feature; ship it close to phase 3.
- Phase 6 is the gate for "any tenant can go live in production". Until phase 6 is in, this is a sandbox-only feature.

## 7. What this lets you (the SACCO) do once shipped

- Stop manual reconciliation of bulk M-PESA paybill statements at month-end.
- Open three paybills (Loans / BOSA / FOSA) and let each one route to the right product without staff intervention.
- Let a member type their member number into M-PESA and have their fees, loan, BOSA, and FOSA serviced in priority order from a single payment.
- Auto-disburse approved loans without an officer logging into the M-PESA business portal.
- Reconcile the books daily against Safaricom's own statement, with diffs raised as workflow tasks the moment they exist.

The architecture is structured so that swapping Safaricom for Airtel Money, Equitel, or a future PesaLink connector later is a connector swap inside `internal/daraja/` — the distribution engine, paybill registry, audit trail, and UI all stay.