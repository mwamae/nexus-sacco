# Phase 1.5a — Collateral foundation (lifecycle, valuation, approval gate, basic UI)

Build the foundation of collateral management: extend the lifecycle from `pledged/released/auctioned` to the full `offered → verified → valued → pledged → released/auctioned` chain, add the per-product `security_model` enum so each product can require guarantors, collateral, both, either, or none, build the Collateral tab on the application/loan detail pages with a policy-aware Security Coverage header, and enforce the security policy at workflow approve time.

This is the minimum viable collateral feature. Officers can add collateral, verify it, attach valuations, and the approval gate blocks under-secured loans. Advanced features (internal-account liens, third-party pledger SMS consent, charge registration, document custody, insurance tracking, reports) ship in **Phase 1.5b**.

## Files to read first

- `services/savings/internal/db/migrations/0005_lending.up.sql:295-312` — current `loan_collateral` table.
- `services/savings/internal/db/migrations/0005_lending.up.sql:118-200` — `loan_products`. Note existing fields like `interest_method`, `multiplier_basis`, `collateral_requirement`.
- `services/savings/internal/handler/loan_application.go::Create` — the only current write path for collateral (in the application-create body).
- `services/savings/internal/handler/loan_application_workflow.go` — the workflow integration where approval is dispatched.
- `services/savings/internal/handler/loan_repayment.go` — pattern for how handler endpoints integrate with the workflow + GL pipeline.
- `services/savings/internal/store/loan_application_store.go` — existing application store.
- `services/savings/internal/store/loan_guarantee_store.go` — for the guarantor-coverage computation (sum of accepted pledges).
- `web/admin/src/pages/Applications/ApplicationDetail.tsx` — where the Collateral tab will land. Currently no reference to "collateral" — this PR adds it.
- `nexusSacco_Loan_Collateral_Benchmark_And_Recommendation.md` — the strategic doc this prompt implements.

## Scope — one PR, six steps

### Step 1 — Schema migration

New migration `services/savings/internal/db/migrations/00NN_collateral_foundation.up.sql`:

```sql
BEGIN;

-- 1. Extend loan_products with security policy.
ALTER TABLE loan_products
  ADD COLUMN IF NOT EXISTS security_model text NOT NULL DEFAULT 'guarantor_only'
    CHECK (security_model IN ('none','guarantor_only','collateral_only','either','both')),
  ADD COLUMN IF NOT EXISTS min_guarantor_cover_pct numeric(5,2) NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS min_collateral_cover_pct numeric(5,2) NOT NULL DEFAULT 125;

COMMENT ON COLUMN loan_products.security_model IS
  'Which external security is required: none, guarantor_only, collateral_only, either (one or the other), both (both required).';

-- 2. Expand loan_collateral lifecycle.
-- Existing status is a free text column; we tighten it. First drop any
-- legacy values, then add the constraint.
UPDATE loan_collateral SET status = 'pledged' WHERE status NOT IN ('pledged','released','auctioned');

ALTER TABLE loan_collateral
  DROP CONSTRAINT IF EXISTS loan_collateral_status_check;
ALTER TABLE loan_collateral
  ADD CONSTRAINT loan_collateral_status_check
  CHECK (status IN ('offered','verified','pledged','released','auctioned'));

-- 3. Add verification + lifecycle audit fields.
ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS proposed_by         uuid,
  ADD COLUMN IF NOT EXISTS proposed_at         timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS verified_by         uuid,
  ADD COLUMN IF NOT EXISTS verified_at         timestamptz,
  ADD COLUMN IF NOT EXISTS verification_notes  text,
  ADD COLUMN IF NOT EXISTS verification_photos jsonb DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS pledged_at          timestamptz,
  ADD COLUMN IF NOT EXISTS pledged_by          uuid,
  ADD COLUMN IF NOT EXISTS released_at         timestamptz,
  ADD COLUMN IF NOT EXISTS released_by         uuid,
  ADD COLUMN IF NOT EXISTS released_reason     text,
  ADD COLUMN IF NOT EXISTS rejected_reason     text;

-- 4. Valuation as a separate table — supports revaluation history.
CREATE TABLE collateral_valuations (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id      uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  valuer_name        text NOT NULL,                                  -- panel valuer / firm
  valuer_contact     text,
  valuation_date     date NOT NULL,
  market_value       numeric(18,2) NOT NULL CHECK (market_value > 0),
  forced_sale_value  numeric(18,2) NOT NULL CHECK (forced_sale_value > 0),
  valuation_report_path text,                                        -- storage path for the PDF
  expires_at         date,                                           -- when revaluation is due
  is_current         boolean NOT NULL DEFAULT true,
  superseded_by_id   uuid REFERENCES collateral_valuations(id),      -- chain when revalued
  notes              text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid NOT NULL
);
CREATE INDEX collateral_valuations_collateral_idx
  ON collateral_valuations (collateral_id, is_current);
CREATE INDEX collateral_valuations_expiring_idx
  ON collateral_valuations (tenant_id, expires_at)
  WHERE is_current = true AND expires_at IS NOT NULL;
-- Only one current valuation per collateral item.
CREATE UNIQUE INDEX collateral_valuations_one_current_idx
  ON collateral_valuations (collateral_id) WHERE is_current = true;
ALTER TABLE collateral_valuations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_valuations ON collateral_valuations
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- 5. Per-tenant operational settings (defaults for new products + tenant-wide ops).
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS default_security_model text NOT NULL DEFAULT 'guarantor_only'
    CHECK (default_security_model IN ('none','guarantor_only','collateral_only','either','both')),
  ADD COLUMN IF NOT EXISTS default_min_guarantor_cover_pct numeric(5,2) NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS default_min_collateral_cover_pct numeric(5,2) NOT NULL DEFAULT 125,
  ADD COLUMN IF NOT EXISTS collateral_revaluation_months int NOT NULL DEFAULT 24;

-- 6. Audit log for every status transition (separate from generic audit
-- because we want a clean drillable timeline).
CREATE TABLE loan_collateral_events (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id  uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  actor_user_id  uuid,
  kind           text NOT NULL CHECK (kind IN (
    'proposed','verified','valued','pledged','released','rejected','auctioned',
    'revalued','documents_attached','note_added'
  )),
  details        jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX loan_collateral_events_collateral_idx
  ON loan_collateral_events (collateral_id, occurred_at DESC);
ALTER TABLE loan_collateral_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_collateral_events ON loan_collateral_events
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

COMMIT;
```

Down migration drops the new objects in reverse dependency.

### Step 2 — Backend handlers

New file `services/savings/internal/handler/collateral.go` mounting on the consolidated workflow + audit pipeline. Endpoints:

| Endpoint | Body | Permission |
| --- | --- | --- |
| `POST   /v1/loan-applications/{app_id}/collateral` | `{kind, description, estimated_value, ownership_path?, photos?}` → status=`offered` | `loans:apply` |
| `GET    /v1/loan-applications/{app_id}/collateral` | list (with current valuation) | `loans:view` |
| `GET    /v1/loans/{loan_id}/collateral` | list (post-disbursement view; same data) | `loans:view` |
| `GET    /v1/collateral/{id}` | full detail incl. valuation history + events | `loans:view` |
| `PATCH  /v1/collateral/{id}` | `{description?, estimated_value?, ownership_path?, photos?}` — editable only in `offered` status | `loans:apply` |
| `POST   /v1/collateral/{id}/verify` | `{notes, photos[]}` → status=`verified` | `loans:verify_collateral` |
| `POST   /v1/collateral/{id}/reject` | `{reason}` → status="offered" stays but `rejected_reason` set; soft reject so officer can resubmit after fixes (or remove the row) | `loans:verify_collateral` |
| `POST   /v1/collateral/{id}/valuation` | `{valuer_name, valuer_contact?, valuation_date, market_value, forced_sale_value, valuation_report_path, expires_at, notes?}` → status moves to `valued` if currently `verified`; creates new valuation row, supersedes any prior current valuation | `loans:value_collateral` |
| `POST   /v1/collateral/{id}/pledge` | `{}` → status moves `valued → pledged`; sets `pledged_at`, `pledged_by` | `loans:approve` |
| `POST   /v1/collateral/{id}/release` | `{reason}` → status `pledged → released`; sets `released_at`, `released_by` | `loans:approve` |
| `DELETE /v1/collateral/{id}` | only valid while `offered` or `verified` (never delete a pledged or released item — audit trail) | `loans:apply` |
| `GET    /v1/loan-applications/{app_id}/security-coverage` | computed coverage card (Step 4) | `loans:view` |

Every mutation writes to `loan_collateral_events` with the right `kind`. The lifecycle is enforced server-side: status transitions are validated against an allowed-from-state matrix; invalid transitions return 409 with the actual current status.

Status matrix:

```
offered    → verified | rejected (soft) | deleted
verified   → valued   | rejected (soft)
valued     → pledged  | revalued (creates new valuation; status stays valued or moves to pledged if already pledged)
pledged    → released | auctioned
released   (terminal)
auctioned  (terminal)
```

### Step 3 — Security coverage computation

New file `services/savings/internal/coverage/coverage.go`:

```go
package coverage

type Coverage struct {
    GuarantorPledged  decimal.Decimal   // sum of accepted loan_guarantees.amount_guaranteed
    CollateralFSV     decimal.Decimal   // sum of pledged collateral's current FSV
    LoanAmount        decimal.Decimal   // application.approved_amount, or requested_amount if not approved yet
}

type Policy struct {
    SecurityModel             string  // 'none' | 'guarantor_only' | 'collateral_only' | 'either' | 'both'
    MinGuarantorCoverPct      decimal.Decimal
    MinCollateralCoverPct     decimal.Decimal
}

type Result struct {
    GuarantorPct       decimal.Decimal   // (pledged / loan_amount) × 100
    CollateralPct      decimal.Decimal   // (fsv / loan_amount) × 100
    GuarantorPasses    bool              // guarantor_pct ≥ min_guarantor_cover_pct
    CollateralPasses   bool              // collateral_pct ≥ min_collateral_cover_pct
    PolicyMet          bool
    Reason             string            // human-friendly summary
}

func Evaluate(c Coverage, p Policy) Result { … }
```

The `PolicyMet` rule per security_model:

- `none`: true (no external security check; multiplier check is orthogonal)
- `guarantor_only`: `GuarantorPasses`
- `collateral_only`: `CollateralPasses`
- `either`: `GuarantorPasses OR CollateralPasses`
- `both`: `GuarantorPasses AND CollateralPasses`

The `Reason` field returns a human-readable summary suitable for the 409 error body and the UI:

- Pass example: `"Policy met (either): collateral coverage 142% ≥ 125%."`
- Fail example: `"Policy not met (both): guarantor coverage 100% ≥ 100% ✓, collateral coverage 80% < 125% ✗ — add KES 225,000 collateral FSV."`

`GET /v1/loan-applications/{app_id}/security-coverage` returns `Coverage + Policy + Result` together so the UI renders the card directly.

Unit test the evaluator extensively — every security_model × pass/fail combination.

### Step 4 — Approval gate enforcement

In `services/savings/internal/handler/loan_application_workflow.go` and `services/savings/internal/handler/wf_callbacks/loan_application_decision.go` (or wherever the workflow's loan-application-decision callback lives):

Before allowing the workflow's approve action to commit, call `coverage.Evaluate(...)`. If `PolicyMet == false`, return a 409 with `Reason` as the body. The workflow's act-on-instance call fails; the inbox UI surfaces the error to the approver.

Edge cases:

- **`none` products**: the gate is a no-op; existing flow continues unchanged.
- **Application has no `approved_amount` yet**: coverage is computed against `requested_amount`. Acceptable for early-stage approvals; final approval re-checks against approved.
- **Approve-with-conditions**: when an approver approves with conditions ("subject to additional guarantor"), the coverage check uses the **already-pledged** amounts; the conditions become items in `loan_applications.approval_conditions` text and must be re-verified before disbursement. Disbursement gates again on the same coverage check at disbursement time — this is the second hard gate.

Add a `loans:override_coverage` permission for senior staff to bypass the gate (write a `loan_coverage_overrides` row with reason + actor). Use sparingly — defaults grant only `tenant_owner` + `sacco_admin`.

### Step 5 — Frontend: Collateral tab

In `web/admin/src/pages/Applications/ApplicationDetail.tsx` AND `web/admin/src/pages/Loans/LoanDetail.tsx` (both — applications pre-disbursement and loans post-disbursement see the same tab), add a Collateral tab.

The tab renders three regions:

**Region A — Security Coverage header card** (policy-aware per §4.6 of the recommendation doc):

```tsx
<SecurityCoverageCard data={coverage} />
```

Display per security_model:

- `none`: collapsed card "No external security required for this product."
- `guarantor_only`: just the guarantor row, collateral row muted "n/a for this product."
- `collateral_only`: the inverse.
- `either`: both rows; status shows which side passes (or both, or neither).
- `both`: both rows; both must show ✓ for overall pass.

Surface the active policy line at the top ("Security policy: EITHER (guarantor 100% OR collateral 125%)"). Status pill at the bottom (`✓ MEETS POLICY` green, `✗ POLICY NOT MET — {Reason}` red, `⚠ partial` amber).

**Region B — Items list**:

```
Kind          Description                       Status      Est. Value      FSV      Actions
Title deed    Plot LR12345, Nakuru             [Pledged]   500,000        400,000  [View] [Release]
Vehicle       2019 Toyota Hilux, KCD 123A      [Valued]    1,200,000      900,000  [Pledge]
Equipment     CNC machine, model X             [Verified]  600,000        —        [Add valuation]
Title deed    Plot LR67890, Eldoret            [Offered]   300,000        —        [Verify] [Reject]
```

Each row has the appropriate actions for its status. Click → slide-over detail panel.

**Region C — "Add collateral" action bar**:

- For applicants/officers in apply-state: visible.
- For loan in `pending_disbursement` or active states: visible (officers can add post-approval collateral; common scenario when extra security is requested).
- For closed/written-off loans: hidden.
- Dropdown: pick kind → modal opens. Modal asks for the fields the kind needs (e.g. `title_deed` asks for LR number, location; `vehicle_logbook` asks for plate, make/model, year). All kinds collect description + estimated_value + ownership document upload.

**Slide-over detail panel** (item detail):

- Header: kind + description + status pill.
- Tabs: Timeline (events from `loan_collateral_events`), Valuation (current + history of prior valuations), Documents (ownership doc, photos), Notes.
- Action bar at bottom — actions appropriate to current status (Verify / Reject / Add valuation / Pledge / Release).
- Each action confirms via inline modal; failures show error inline (not a toast).

### Step 6 — Settings: per-product security policy

In `web/admin/src/pages/LoanProducts.tsx` (the product editor), add a Security section to the create/edit form:

- **Security model**: radio group (None / Guarantors only / Collateral only / Either / Both). Tooltip explains each.
- **Min guarantor coverage %**: shown when model is `guarantor_only | either | both`. Default 100.
- **Min collateral coverage %**: shown when model is `collateral_only | either | both`. Default 125.
- **Collateral kinds accepted**: multi-select against the existing `loan_collateral_kind` enum — restricts which kinds count for this product (e.g. a property loan accepts only `title_deed`). Stored as new column `loan_products.accepted_collateral_kinds text[]`. Default `NULL` = all kinds.

In Settings → Operations, add a Defaults sub-section with the tenant_operations defaults (default_security_model + the two cover defaults + revaluation months).

### Step 7 — Tests + observability

Go:

- `coverage_test.go` — table-driven across every security_model × pass/fail.
- `collateral_lifecycle_test.go` — every status transition (allowed + forbidden).
- `collateral_approval_gate_test.go` — workflow approve action with under-secured loan blocks; with `loans:override_coverage` permission unblocks; with `none` security_model is unaffected.
- `collateral_valuation_test.go` — multiple valuations supersede correctly; only one `is_current` per item.

React:

- `SecurityCoverageCard.test.tsx` — renders correctly for each security_model.
- `CollateralTab.test.tsx` — items render, actions appropriate for each status, slide-over opens.
- `LoanProducts.security.test.tsx` — product editor security section.

Audit / observability:

- Event keys: `loan.collateral.proposed`, `verified`, `valued`, `pledged`, `released`, `rejected`, `revalued`, `coverage_override`.
- Metric: `loan_applications_blocked_by_coverage_total{security_model}` — tracks how often the gate fires (helps tenants tune their cover thresholds).

## Acceptance walkthrough

1. As `branch_manager`, edit an existing loan product → set Security model to `either`, min guarantor 100, min collateral 125. Save.
2. Member applies for a KES 200,000 loan under that product. No guarantors yet, no collateral yet. Open the application → Collateral tab. Header card shows "Status: ✗ POLICY NOT MET. Add guarantor coverage or collateral FSV to meet the either-rule (need 100% guarantor OR 125% collateral)."
3. Add a guarantor (existing flow) pledging KES 100,000. Coverage now: guarantor 50%, collateral 0%. Status still ✗.
4. Click "Add collateral" → Title deed → description, estimated 300,000, upload photo of title. Row appears in status `Offered`.
5. As `credit_officer`, click Verify → notes + 2 inspection photos → status flips to `Verified`. Event appears in timeline.
6. Click "Add valuation" → enter market_value 300,000, FSV 250,000, valuer name, expires_at +24mo, attach PDF report. Status flips to `Valued`. Header card now: collateral 125%, guarantor 50%, status ✓ MEETS POLICY (passes on collateral side).
7. Submit the application for approval. Approver in the inbox clicks Approve. Workflow advances. Final approval fires; coverage re-checked; passes; loan moves to `pending_disbursement`.
8. Counter-test: change the product's `security_model` to `both`. Re-evaluate coverage on the same application → fails (guarantor 50% < 100%). Approver clicks Approve → workflow returns 409 with the human-readable reason. Approver adds more guarantors → 100% → re-tries → passes.
9. Disburse the loan. Coverage card stays visible on `/loans/register/{id}` Collateral tab. Click on the title-deed item → slide-over → timeline shows the full chain (proposed → verified → valued → pledged).
10. Settle the loan. The release flow on each pledged collateral item triggers (released_at, released_by, reason). Status flips to `Released`. Items remain visible but read-only.

## Idempotency / safety

- Status transitions enforce the allowed-from matrix server-side. Invalid transitions → 409 with current status.
- Valuation `is_current` constraint enforced via partial unique index — at most one current valuation per collateral item.
- Approval gate is a hard block at workflow approve-action time. Override available only to roles with `loans:override_coverage` and writes a permanent audit row.
- Disbursement is the second gate — re-checks coverage. If conditions of approval weren't met (collateral wasn't actually pledged), disbursement refuses. This is the safety net for "approver clicked approve in good faith but the operational team didn't follow through."
- All existing loans without any collateral default to security_model='guarantor_only' (via the product default) and continue working unchanged. No data migration needed for existing loans.
- `gofmt`, `go vet`, full `go test ./services/savings/...`, `pnpm test`, `pnpm build` all green.

When you're done, paste into the PR description: a screenshot of the Collateral tab with the Security Coverage card in three states (passes, partial, fails), the product editor's Security section, the approver's 409 error showing the policy reason, and the diff stat.