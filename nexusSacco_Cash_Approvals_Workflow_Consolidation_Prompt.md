# Two approval systems → one — review and consolidation prompt

## What I found

You have two approval systems running side by side. The user's intuition is correct: there's an architectural drift the codebase has been half-fixing for a while, and the migration isn't finished.

### System A — Cash Approvals (the legacy, single-level system)

- **Page**: `/cash-approvals` (`web/admin/src/pages/CashApprovals.tsx`).
- **Backend**: `pending_approvals` table in the savings DB (migration `services/savings/internal/db/migrations/0010_pending_approvals.up.sql`).
- **Model**: one maker, one checker. **No levels, no roles, no SLA, no conditional routing.** Per-kind toggles live on `tenant_operations.approval_deposit / approval_withdrawal / approval_share_purchase / …` — each is just a boolean.
- **Kinds**: `deposit, withdrawal, deposit_transfer, share_purchase, share_transfer, share_bonus, loan_disbursement, loan_repayment, loan_settle, loan_reverse, loan_writeoff, loan_reschedule, loan_moratorium, loan_settlement_discount, fee, welfare, application_fee` — these are the `ApprovalKind` enum values, all single-level.
- **Executor**: `services/savings/internal/handler/pending_approvals.go::executePayloadTx` — switches on kind, runs the executor + GL post inline.

The `CashApprovals.tsx` page already has a `DeprecationBanner` at line 65 ("Heads up — cash approvals have moved. Decisions now live in the unified Approvals Inbox. This page is read-only and will be removed in a future release.") — confirming this system has been marked for retirement for some time.

### System B — Workflow Inbox + Definitions (the canonical, multi-level system)

- **Pages**: `/approvals` (Inbox; `web/admin/src/pages/ApprovalsInbox.tsx`) and `/workflows` (Definitions admin; `web/admin/src/pages/WorkflowDefinitions.tsx`).
- **Backend**: `wf_definitions` + `wf_levels` + `wf_instances` + `wf_actions` (services/workflow). Migration `services/workflow/internal/db/migrations/0003_seed_process_kinds.up.sql` seeds the multi-level definitions for each tenant.
- **Model**: per-tenant **definition with N levels**, each level has roles, quorum (`any_one | all | majority`), SLA hours, optional conditional escalation (e.g. `amount > 500000 → Board level kicks in`). The Inbox lets users claim, approve, reject, return-for-correction, request-info, escalate, reassign, cancel, resume. Threaded comments. Bulk decide for safe kinds.
- **Process kinds today (seeded in migration 0003)**: `cash_deposit, cash_withdrawal, cash_account_transfer, share_purchase, share_transfer, share_bonus_issue, loan_application_decision, loan_reschedule, loan_moratorium, loan_write_off, loan_settlement_discount, member_blacklist, member_close` — plus the newer additions for `mpesa_b2c_reversal, mpesa_unallocated_reconciliation, fiscal_year_close, journal_close`.
- **Feature flag**: `tenants.unified_inbox_enabled` (migration `services/identity/internal/db/migrations/0020_unified_inbox_flag.up.sql`). Defaults `false`. Some flows (loan applications, fiscal year close, M-PESA reversals) already key off this flag and use the workflow system when on; otherwise fall back to the legacy pending_approvals path.

### The state today

Two parallel systems exist because the migration started but never finished. Today:

- **Loan applications, fiscal-year close, journal close, M-PESA b2c reversal, M-PESA unallocated reconciliation** → workflow system (gated on `unified_inbox_enabled`).
- **Everything cash-related** (deposit, withdrawal, share purchase, share transfer, share bonus, loan repayment, loan settle, loan reverse, loan writeoff, loan reschedule, loan moratorium, loan settlement discount, fee, welfare, application fee) → **still uses the legacy single-level `pending_approvals` system**, even when `unified_inbox_enabled = true`.

So your specific complaint — "I wanted 4 levels of approval but it wasn't adopted under cash approval" — is exactly the gap. The workflow definitions for `cash_deposit` etc. are seeded, but the savings service still queues into `pending_approvals` instead of `wf_instances` when the toggle is on, and the executor still runs `pending_approvals.executePayloadTx` instead of the workflow `act_on_instance` handler. The user-visible result: a 4-level definition you configure in `/workflows` for `cash_deposit` is never consulted, because the cash-deposit endpoint never creates an instance against that definition.

---

## Recommended consolidation

**Keep System B (workflow). Decommission System A (cash approvals).** This was the intended end-state from the start; the current half-state is the result of an incomplete migration. The new system is strictly more capable: it supports everything the old one did (single-level is `levels: [{quorum: any_one, …}]`) and adds the multi-level + SLA + conditional + comments features the user explicitly wanted.

The consolidation has six parts. They land in this order to keep every intermediate state shippable.

### Part 1 — Map every legacy `ApprovalKind` to a workflow `process_kind`

```
ApprovalKind                  →  workflow process_kind            seeded today?
─────────────────────────────────────────────────────────────────────────────
deposit                       →  cash_deposit                     ✓
withdrawal                    →  cash_withdrawal                  ✓
deposit_transfer              →  cash_account_transfer            ✓
share_purchase                →  share_purchase                   ✓
share_transfer                →  share_transfer                   ✓
share_bonus                   →  share_bonus_issue                ✓
share_lien                    →  share_lien                       ⚠ add
loan_disbursement             →  loan_disbursement                ⚠ add (only loan_application_decision seeded)
loan_repayment                →  loan_repayment                   ⚠ add
loan_settle                   →  loan_settle                      ⚠ add
loan_reverse                  →  loan_reverse                     ⚠ add
loan_writeoff                 →  loan_write_off                   ✓
loan_reschedule               →  loan_reschedule                  ✓
loan_moratorium               →  loan_moratorium                  ✓
loan_settlement_discount      →  loan_settlement_discount         ✓
fee                           →  fee_posting                      ⚠ add
welfare                       →  welfare_posting                  ⚠ add
application_fee               →  application_fee                  ⚠ add
fee_posting / welfare_posting →  (alias of fee / welfare)         (same)
```

Eight new `process_kind`s need seeding in a new migration. Each defaults to a **single-level** definition that mirrors today's toggle-on behaviour (one checker, `branch_manager` role, `any_one`, no SLA). Tenants who want 4 levels then edit the definition in `/workflows`.

### Part 2 — Replace `Approvals.QueueTx` in the savings handlers

Every place that calls `h.Approvals.QueueTx(...)` (savings's pending_approvals queue) becomes a `workflowclient.CreateInstanceTx(...)` call instead — same outer tx. The payload moves from `pending_approvals.payload` into `wf_instances.context`. The subject mapping is straightforward:

```go
// before
pa, _ := h.Approvals.QueueTx(ctx, tx, store.QueueInput{
    Kind: domain.ApprovalKindDeposit,
    Title: …,
    SubjectMemberID: &memberID,
    Amount: &amt,
    Payload: payload,
    MakerUserID: userID,
})

// after
inst, _ := h.Workflow.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
    TenantID:    tid,
    ProcessKind: "cash_deposit",
    SubjectKind: "deposit_account",
    SubjectID:   accountID,
    MakerUserID: userID,
    Summary:     fmt.Sprintf("Deposit to a/c %s — KES %s", acct.AccountNo, amt.StringFixed(2)),
    Amount:      &amt,
    Context:     payloadAsMap,   // same JSON
    SourceURL:   "/deposits/" + accountID.String(),
})
```

The toggle check changes too: today it's `if toggles.Deposit { queue }`. Tomorrow it's `if h.Workflow.HasActiveDefinitionTx(ctx, tx, "cash_deposit") { queue }`. A definition existing with at least one level == "approval required"; no active definition == "post immediately." Toggles in `tenant_operations.approval_*` columns can be derived (or removed entirely — see Part 5).

### Part 3 — Move the executor from `pending_approvals.executePayloadTx` to the workflow `act_on_instance` callback

The workflow service already supports a per-process-kind action callback (the place where "this approval reached its final level → execute" lives). Today the M-PESA, fiscal-year-close, and journal-close flows use this. Add a callback per migrated kind that runs the same body as the corresponding `case domain.ApprovalKindDeposit` branch of `executePayloadTx` does today (it already calls the right `ExecuteDepositTx` + `postingops.PostDepositTx` + `receiptops.WriteTx`).

The callback registration lives in `services/savings/cmd/server/main.go` — register one callback per `process_kind` against the workflow client's dispatcher. When the workflow's terminal action fires, the callback runs inside the workflow's outer tx (so atomic with the wf_instance status flip + the GL post + the receipt + the subledger write).

### Part 4 — Migrate in-flight `pending_approvals` rows to `wf_instances`

The migration script walks `pending_approvals WHERE status = 'pending'`, creates a matching `wf_instance` per row, copies payload → context, links via a new `wf_instances.legacy_pending_approval_id` column for audit, then marks the legacy row `migrated`. Approvers see the same items in the Inbox after the cutover.

### Part 5 — Decommission `/cash-approvals` and the legacy toggle table

- Replace the `/cash-approvals` route in `App.tsx` with a 301 redirect to `/approvals` filtered by the cash-kind process kinds.
- Update the existing `DeprecationBanner` to be the ONLY thing the page renders (no more legacy queue list).
- Settings → Approvals → the toggle list either (a) becomes a "configure" link per kind that opens that kind's wf_definition in `/workflows`, or (b) stays as a simple "approval required: yes/no" toggle that toggles the active flag on the corresponding wf_definition (the simple-mode users want).
- After two release cycles with the redirect, delete the `CashApprovals.tsx` page entirely and the `pending_approvals` table (DROP TABLE in a destructive migration with an explicit "data migrated to wf_instances on date X" comment in the down script).

### Part 6 — The `unified_inbox_enabled` flag becomes the default

The flag exists because the migration was rolling out per-tenant. After Parts 1–5 land, every tenant uses the workflow system. The flag's last job is "did this tenant migrate its in-flight pending_approvals yet" — once Part 4's backfill runs, every tenant's flag flips to true, and the flag is retired in a follow-up migration.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. There are two approval systems running side by side: the legacy single-level `pending_approvals` (savings) and the canonical multi-level workflow engine (workflow service). The migration is half-done; cash kinds (deposit, withdrawal, share purchase, loan repayment, etc.) still use the legacy system even when the tenant has `unified_inbox_enabled = true`. Finish the migration in one PR: every cash kind queues into `wf_instances`, the legacy table goes read-only, the `/cash-approvals` page redirects to `/approvals`. Existing in-flight pending rows migrate cleanly. Settings → Approvals keeps its simple toggle UX but drives wf_definitions under the hood.
>
> **Scope** — six parts, in order. Each is shippable on its own.
>
> **Files you will read first**
>
> - `services/savings/internal/db/migrations/0010_pending_approvals.up.sql` — legacy schema.
> - `services/savings/internal/handler/pending_approvals.go` — the `executePayloadTx` switch (lines ~570–910) is the function we're decomposing.
> - `services/savings/internal/handler/deposit.go::Deposit` (≈line 372) — example of `Approvals.QueueTx` calls being replaced.
> - `services/savings/internal/handler/share.go::Purchase`, `loan_repayment.go::PostRepayment`, `collection_desk.go::CreateReceipt` — same pattern; cover them all.
> - `services/workflow/internal/db/migrations/0003_seed_process_kinds.up.sql` — current seeded kinds + the `_wf_seed_one` SQL helper.
> - `services/workflow/internal/handler/instance.go::CreateInstance, ActOnInstance` — the workflow API.
> - `services/savings/internal/workflowclient/client.go` (if it exists; check) — or `services/mpesa/internal/workflowclient/client.go` as the template if savings doesn't have one yet.
> - `services/identity/internal/db/migrations/0020_unified_inbox_flag.up.sql` — the rollout flag.
> - `web/admin/src/pages/CashApprovals.tsx`, `ApprovalsInbox.tsx`, `WorkflowDefinitions.tsx`, `App.tsx` (route table), `components/AppShell.tsx` (nav).
>
> ### Part 1 — Seed the missing process kinds
>
> New migration `services/workflow/internal/db/migrations/0010_seed_cash_process_kinds.up.sql`. For every tenant, seed single-level definitions for:
>
> `share_lien, loan_disbursement, loan_repayment, loan_settle, loan_reverse, fee_posting, welfare_posting, application_fee`
>
> Each gets one level named "Checker", roles `['branch_manager']`, quorum `any_one`, `sla_hours: 24`, no condition. Mirror the existing `_wf_seed_one` helper calls from migration 0003. Down migration removes them.
>
> Confirm the existing seeded kinds (`cash_deposit, cash_withdrawal, cash_account_transfer, share_purchase, share_transfer, share_bonus_issue, loan_write_off, loan_reschedule, loan_moratorium, loan_settlement_discount`) cover the rest of the legacy `ApprovalKind` enum. If any are missing, add them in the same migration.
>
> ### Part 2 — Migrate `Approvals.QueueTx` callers
>
> Find every `h.Approvals.QueueTx` call in `services/savings/`. There are ~14 of them — every `case` branch in the relevant handlers (`deposit.go, withdraw, share.go, loan_repayment.go, loan_settle, loan_reverse, loan_writeoff, loan_reschedule, loan_moratorium, loan_settlement_discount, collection_desk.go fee/welfare lines, application_fee_executor.go`).
>
> For each, replace with:
> ```go
> if h.Workflow != nil && h.Workflow.HasActiveDefinitionTx(ctx, tx, tid, processKindForApprovalKind(kind)) {
>     inst, qerr := h.Workflow.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
>         TenantID:    tid,
>         ProcessKind: processKindForApprovalKind(kind),
>         SubjectKind: subjectKindFor(kind),
>         SubjectID:   subjectID,
>         MakerUserID: userID,
>         Summary:     summary,
>         Amount:      &amt,
>         Context:     payload,                       // jsonb
>         SourceURL:   sourceURLFor(kind, subjectID),
>     })
>     // …
> }
> ```
>
> Helper `processKindForApprovalKind(k domain.ApprovalKind) string` lives in `services/savings/internal/handler/approval_kind_map.go` (new file). Same shape with `subjectKindFor` and `sourceURLFor`.
>
> If `HasActiveDefinitionTx` returns false (no active wf_definition), post inline as today's toggle-off path does.
>
> The legacy `Approvals.QueueTx` path stays compilable in this PR — call sites move off it but the function stays. Remove it in Part 5.
>
> ### Part 3 — Move execution from `executePayloadTx` to workflow callbacks
>
> The workflow service supports terminal-action callbacks per `process_kind`. Wire one callback per migrated kind, registered in `services/savings/cmd/server/main.go` via `workflowclient.RegisterTerminalAction(processKind, func(ctx, tx, instance) error { … })`. Each callback runs the same body as the corresponding `case` branch of `pending_approvals.executePayloadTx` today (which already correctly composes `Execute…Tx` + `postingops.PostXxx` + `receiptops.WriteTx` after the previous PR).
>
> The workflow's `ActOnInstance` already opens a tx and fires the callback inside it. Don't reach back into `pending_approvals.executePayloadTx`; that file gets deleted in Part 5. Instead, **extract each branch into its own file**:
>
> - `services/savings/internal/handler/wf_callbacks/deposit.go` — the body of the `case domain.ApprovalKindDeposit:` branch.
> - `services/savings/internal/handler/wf_callbacks/share_purchase.go`
> - etc.
>
> Each function signature is `func(ctx context.Context, tx pgx.Tx, deps Deps, instance *workflowclient.Instance) error`. `Deps` carries the same handles `pending_approvals.go` had today (`Deposit *DepositHandler, Share *ShareHandler, …`). Registration in `main.go` is a 20-line block — one `RegisterTerminalAction` per kind.
>
> **The propagation step** (`propagateToReceiptLine`) that updated the Collection Desk receipt line on terminal action moves into the callback too — when the workflow instance was queued from a receipt line, the callback flips that line to posted. Detect via `instance.Context["receipt_line_id"]` which the queue-time call from Part 2 already populates for collection-desk-originated approvals.
>
> ### Part 4 — Migrate in-flight `pending_approvals` rows
>
> New script `services/savings/cmd/legacy-approvals-migrate/main.go`:
>
> 1. Iterate every tenant.
> 2. For each, `SELECT * FROM pending_approvals WHERE status = 'pending' ORDER BY created_at`.
> 3. For each row, create the matching `wf_instance` via `Workflow.CreateInstanceTx`, copying `payload → context`, `kind → process_kind` via the same `processKindForApprovalKind` mapping, maker user, subject ids. Stamp `wf_instances.legacy_pending_approval_id = pa.id` (new nullable column added in this migration).
> 4. Update `pending_approvals.status = 'migrated'` (extend the CHECK constraint to allow it).
> 5. Idempotent: re-running skips rows already marked `migrated`.
>
> Flag: `--apply` performs the migration; default is dry-run + report. **The PR's release notes call this out**: "First boot after this PR runs `legacy-approvals-migrate --apply` against every tenant; existing in-flight approvals show up in /approvals automatically."
>
> ### Part 5 — Decommission /cash-approvals page + legacy queue infra
>
> 1. Replace `web/admin/src/pages/CashApprovals.tsx` body with a **301 redirect**:
>    ```tsx
>    export default function CashApprovals() {
>      useEffect(() => {
>        window.location.replace('/approvals?kinds=cash_deposit,cash_withdrawal,cash_account_transfer,share_purchase,…');
>      }, []);
>      return <div className="empty">Redirecting to the unified Approvals Inbox…</div>;
>    }
>    ```
>    Don't delete the page yet — keep the route as a redirect for one release so bookmarks and external links still work.
> 2. **Settings → Approvals** (`web/admin/src/pages/TenantSettings.tsx` or wherever the toggles live). Each toggle row becomes:
>    - Label: "Approval required" + simple on/off toggle.
>    - When toggling on → `POST /v1/workflows/definitions/{process_kind}/activate` (uses existing endpoint; creates or activates the default 1-level definition).
>    - When toggling off → `POST /v1/workflows/definitions/{process_kind}/deactivate`.
>    - A small "Configure levels →" link that deep-links to `/workflows#{process_kind}` for tenants who want multi-level setup.
> 3. The `tenant_operations.approval_*` boolean columns become **read-only derived views** of the wf_definition active state. Add a database view `tenant_approval_toggles` that exposes the legacy column names for any remaining external callers; deprecate the underlying columns in a follow-up.
> 4. Remove `Approvals.QueueTx` and `pending_approvals.executePayloadTx` from `services/savings/`. The file `pending_approvals.go` shrinks to just the migration helper + reads of historical rows for audit.
> 5. AppShell nav: remove the "Cash approvals" link (if one exists). The Approvals Inbox link stays.
>
> ### Part 6 — Retire `unified_inbox_enabled`
>
> After Part 4's migration script has run for every tenant, the flag's role is over. New migration:
> ```sql
> -- Flag retired — every tenant uses the workflow inbox unconditionally
> -- as of this release. The column stays for one release as
> -- belt-and-suspenders so a panic-revert can flip a tenant back to the
> -- legacy path (which still exists as read-only).
> COMMENT ON COLUMN tenants.unified_inbox_enabled IS 'DEPRECATED — every tenant uses the workflow inbox as of release vX. Column removed in the next major.';
> ```
>
> Find every `unified_inbox_enabled` check in the codebase (grep across savings, accounting, mpesa, member, identity, web/admin) and remove the legacy-path branch. There are ~10 places per the earlier inventory.
>
> Two-release plan: this PR retires the *usage* of the flag (every code path acts as if it's true). The next release deletes the column.
>
> ### Tests
>
> Go:
> - `services/savings/internal/handler/wf_callbacks/*_test.go` — one per callback. Mock the workflow service, fire a terminal action, assert (a) the matching `Execute…Tx` ran, (b) `postingops.PostXxxTx` queued the outbox row, (c) `receiptops.WriteTx` wrote the receipt, (d) audit row exists.
> - `services/savings/cmd/legacy-approvals-migrate/main_test.go` — fixture with 10 pending_approvals across 3 tenants, run --apply, assert all migrated, status updated, idempotent on second run.
> - `services/workflow/internal/handler/instance_test.go` — already covers wf engine; add a regression that asserts the new process_kinds from Part 1's migration are seeded for every tenant.
>
> React:
> - `web/admin/src/pages/CashApprovals.redirect.test.tsx` — asserts the redirect happens.
> - `web/admin/src/pages/TenantSettings.approvals.test.tsx` — toggling a kind on/off calls the wf-definition activate/deactivate endpoint.
>
> ### Acceptance walkthrough
>
> 1. Pre-PR: configure 4 levels for `cash_deposit` in `/workflows` for a test tenant. Make a deposit. Today the deposit goes through the single-level pending_approvals (only the first level fires; the next 3 are ignored).
> 2. Post-PR: same setup. Make a deposit. The Approvals Inbox shows the instance at Level 1. Approve as Level 1. Instance progresses to Level 2. Approve as Level 2. Instance progresses to Level 3. Approve. Level 4. Approve. Only on the final approval does `ExecuteDepositTx` + the GL post + the receipt write fire (atomically).
> 3. Conditional escalation: configure Level 2 to apply only when `amount > 100000`. Make a 50k deposit — workflow jumps from Level 1 straight to Level 3 (skipping Level 2 per the condition). Make a 200k deposit — Level 2 kicks in.
> 4. SLA: Level 1's `sla_hours: 4` — leave a pending instance idle. After 4h, the Inbox shows it red-bordered (overdue), matching today's existing SLA UX.
> 5. Settings → Approvals → toggle "Approval required" off for `share_purchase`. Next share purchase posts immediately (no instance created).
> 6. Visit `/cash-approvals` — instant redirect to `/approvals?kinds=…`.
> 7. The legacy `pending_approvals` table SELECT shows in-flight rows now have `status = 'migrated'`. The matching `wf_instances` rows exist with `legacy_pending_approval_id` populated.
>
> ### Idempotency / safety
>
> - Part 1's migration is additive (`ON CONFLICT DO NOTHING`).
> - Part 2's branch logic is `if wf_definition exists, queue there; else inline` — falls back to today's "no approval needed" behaviour cleanly if a kind is somehow not yet seeded.
> - Part 3's terminal-action callbacks are registered at process start. Failure to register a callback (missing handler dep) fails the server boot loud — no silent no-op.
> - Part 4 is idempotent + dry-run by default.
> - Part 5's CashApprovals redirect is a `window.location.replace` — preserves browser history for back-button.
> - `gofmt`, `go vet`, full `go test ./services/savings/... ./services/workflow/... ./services/accounting/...`, `pnpm test`, `pnpm build` all green.
>
> When done, paste into the PR description: the kind-mapping table from Part 1, the new wf_callbacks file list from Part 3, the legacy-migrate report output from Part 4's --apply run, and screenshots of the 4-level approval flow from the acceptance walkthrough.

---

## Why this is the right consolidation

The user wanted multi-level approvals on cash transactions; that's what the workflow system was designed for. The cash approvals page was the first version, kept around as scaffolding while the migration finished. The DeprecationBanner in the codebase ("This page is read-only and will be removed in a future release") tells you the previous team agreed — they just never got round to actually doing the migration on the cash kinds.

Three structural pieces are worth defending:

**One executor seam — workflow callbacks.** Today `pending_approvals.executePayloadTx` is a 400-line switch statement that everyone scrolls past. Splitting each kind's execution into its own `wf_callbacks/X.go` file makes the executor surface grep-friendly, testable in isolation, and impossible to silently skip (Part 3's registration check fails fast on boot if a callback is missing).

**Simple toggle → workflow definition activation.** The Settings → Approvals toggle UX stays exactly as it is today (`Approval required: on/off` per kind), but the toggle now drives the wf_definition's `active` flag instead of a `tenant_operations.approval_X` boolean. Tenants who want 4 levels click "Configure levels →" and land in the workflow editor. Tenants who don't never see the complexity. Same UX, vastly more capable underlying engine.

**Legacy table stays read-only for one release.** Don't drop `pending_approvals` in this PR. Keep it as a historical record + emergency revert lane while the new world bakes. Drop it in the next major after Part 4's migration is confirmed clean across every tenant.