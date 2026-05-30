# Phase 1.5b — Collateral advanced features (internal liens, third-party pledger, custody, insurance, charge, reports)

Layer the bank-grade depth on top of Phase 1.5a's foundation. This PR adds: first-class liens on deposit and share accounts (so a `fixed_deposit_lien` collateral kind actually locks the deposit), the third-party pledger flow with SMS consent (reuses the guarantor consent SMS infrastructure), document custody tracking (signed in / out per movement), charge registration tracking (Lands Registry / NTSA references), insurance tracking with expiry reminders, the daily revaluation-reminder worker, the auction event log, and the five collateral reports under `/loans/reports`.

## Files to read first

- Phase 1.5a output — confirm `loan_collateral`, `collateral_valuations`, `loan_collateral_events`, `loan_products.security_model` are in place.
- `nexusSacco_Loans_Phase_5_TopUp_Refinance_CheckOff_GroupLoans_Prompt.md` — the BOSA-liens table from Phase 5. This PR's deposit-lien mechanism follows the same shape.
- `services/savings/internal/handler/bosa_exit.go` — the existing pattern for blocking withdrawals against active liens. We extend the same check.
- The guarantor consent SMS prompt (the simple-prompt feature you spec'd earlier) — the third-party pledger SMS reuses that token + OTP + branch-fallback design.
- `services/savings/internal/handler/deposit.go::Withdraw` — withdrawal handler that needs to check liens.
- `services/notification/internal/sms/sender.go` + `internal/pdf/` — the SMS + PDF infra reused for revaluation/insurance reminders.

## Scope — one PR, six steps

### Step 1 — Schema additions

New migration `services/savings/internal/db/migrations/00NN_collateral_advanced.up.sql`:

```sql
BEGIN;

-- 1. Third-party pledger — collateral can be pledged by someone other than the borrower.
ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS pledger_counterparty_id uuid REFERENCES counterparties(id) ON DELETE RESTRICT,
  ADD COLUMN IF NOT EXISTS pledger_consent_status text DEFAULT NULL
    CHECK (pledger_consent_status IN (NULL, 'pending','accepted','declined','offline_consented')),
  ADD COLUMN IF NOT EXISTS pledger_consent_at timestamptz,
  ADD COLUMN IF NOT EXISTS pledger_consent_doc_path text;
COMMENT ON COLUMN loan_collateral.pledger_counterparty_id IS
  'NULL = self-pledge by the borrower. Non-NULL = third-party pledger; consent flow required before pledged state.';

-- 2. Deposit / share-account lien linkage. The fixed_deposit_lien and
--    listed_shares collateral kinds graduate from descriptive text to
--    enforceable liens on real accounts.
CREATE TABLE collateral_deposit_liens (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  deposit_account_id  uuid NOT NULL REFERENCES deposit_accounts(id) ON DELETE RESTRICT,
  liened_amount       numeric(18,2) NOT NULL CHECK (liened_amount > 0),
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','partially_released','released','exercised')),
  placed_at           timestamptz NOT NULL DEFAULT now(),
  released_at         timestamptz,
  exercised_at        timestamptz,
  exercise_reason     text,
  UNIQUE (collateral_id)                                              -- one lien per collateral row
);
CREATE INDEX collateral_deposit_liens_account_active_idx
  ON collateral_deposit_liens (deposit_account_id)
  WHERE status IN ('active','partially_released');
ALTER TABLE collateral_deposit_liens ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_deposit_liens ON collateral_deposit_liens
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE TABLE collateral_share_pledges (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  share_account_id    uuid NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
  pledged_share_count int  NOT NULL CHECK (pledged_share_count > 0),
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','partially_released','released','exercised')),
  placed_at           timestamptz NOT NULL DEFAULT now(),
  released_at         timestamptz,
  exercised_at        timestamptz,
  exercise_reason     text,
  UNIQUE (collateral_id)
);
CREATE INDEX collateral_share_pledges_account_active_idx
  ON collateral_share_pledges (share_account_id)
  WHERE status IN ('active','partially_released');
ALTER TABLE collateral_share_pledges ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_share_pledges ON collateral_share_pledges
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- 3. Charge registration tracking — for kinds that require legal filing.
CREATE TYPE charge_registry AS ENUM (
  'lands_registry', 'ntsa', 'stockbroker_custodian', 'kra', 'other'
);

ALTER TABLE loan_collateral
  ADD COLUMN IF NOT EXISTS charge_registry        charge_registry,
  ADD COLUMN IF NOT EXISTS charge_reference       text,                 -- registry's filing number
  ADD COLUMN IF NOT EXISTS charge_registered_at   timestamptz,
  ADD COLUMN IF NOT EXISTS charge_registered_by   uuid,
  ADD COLUMN IF NOT EXISTS charge_discharge_ref   text,
  ADD COLUMN IF NOT EXISTS charge_discharged_at   timestamptz,
  ADD COLUMN IF NOT EXISTS charge_certificate_path text;               -- the registry-stamped doc

-- 4. Insurance tracking — for kinds that require insurance.
CREATE TABLE collateral_insurance_policies (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  provider_name       text NOT NULL,
  policy_no           text NOT NULL,
  effective_from      date NOT NULL,
  effective_to        date NOT NULL,
  premium_amount      numeric(18,2),
  sum_insured         numeric(18,2) NOT NULL,
  status              text NOT NULL DEFAULT 'active'
    CHECK (status IN ('active','expired','cancelled')),
  is_current          boolean NOT NULL DEFAULT true,
  policy_doc_path     text,
  notes               text,
  created_at          timestamptz NOT NULL DEFAULT now(),
  created_by          uuid NOT NULL
);
CREATE INDEX collateral_insurance_expiring_idx
  ON collateral_insurance_policies (tenant_id, effective_to)
  WHERE is_current = true AND status = 'active';
CREATE UNIQUE INDEX collateral_insurance_one_current_idx
  ON collateral_insurance_policies (collateral_id) WHERE is_current = true;
ALTER TABLE collateral_insurance_policies ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_insurance ON collateral_insurance_policies
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- 5. Document custody — chain of who's holding the original document.
CREATE TABLE collateral_document_custody (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  document_kind       text NOT NULL,                                    -- 'original_title', 'logbook', 'valuation_report', etc.
  movement            text NOT NULL CHECK (movement IN ('checked_in','checked_out','returned_to_borrower')),
  movement_at         timestamptz NOT NULL DEFAULT now(),
  movement_by         uuid NOT NULL,
  custodian_user_id   uuid,                                            -- staff currently holding the doc
  borrower_signature_path text,                                        -- signed receipt
  location_code       text,                                            -- 'BRANCH_NRB_SAFE', 'HQ_VAULT_2A'
  notes               text
);
CREATE INDEX collateral_document_custody_collateral_idx
  ON collateral_document_custody (collateral_id, movement_at DESC);
ALTER TABLE collateral_document_custody ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_collateral_document_custody ON collateral_document_custody
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- 6. Auction events — track the disposal path.
CREATE TABLE collateral_auction_events (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  collateral_id       uuid NOT NULL REFERENCES loan_collateral(id) ON DELETE CASCADE,
  loan_id             uuid REFERENCES loans(id),
  event_kind          text NOT NULL CHECK (event_kind IN (
    'handover_to_auctioneer','auction_notice_published','auction_held',
    'sold','reserve_not_met','rescheduled','proceeds_received'
  )),
  occurred_at         timestamptz NOT NULL DEFAULT now(),
  amount              numeric(18,2),                                    -- for 'sold' / 'proceeds_received'
  buyer_details       text,
  auctioneer_name     text,
  notes               text,
  doc_path            text,                                             -- auction notice, sale agreement, receipt
  created_by          uuid NOT NULL
);
CREATE INDEX collateral_auction_events_loan_idx
  ON collateral_auction_events (loan_id, occurred_at DESC);

-- 7. Charge + insurance + revaluation policy per tenant (extend Phase 1.5a settings).
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS collateral_charge_required_kinds text[] NOT NULL DEFAULT ARRAY['title_deed','vehicle_logbook'],
  ADD COLUMN IF NOT EXISTS collateral_insurance_required_kinds text[] NOT NULL DEFAULT ARRAY['title_deed','vehicle_logbook','equipment'],
  ADD COLUMN IF NOT EXISTS collateral_revaluation_warning_days int NOT NULL DEFAULT 60,
  ADD COLUMN IF NOT EXISTS collateral_insurance_warning_days int NOT NULL DEFAULT 30;

COMMIT;
```

Down migration drops in reverse dependency, restoring `loan_collateral` to its post-Phase-1.5a state.

### Step 2 — Internal-account lien hooks

Two integration points pull the new lien tables into the existing deposit + share withdrawal paths.

**Deposit withdrawal check.** In `services/savings/internal/handler/deposit.go::Withdraw` and `bosa_exit.go::RequestExit`, add a lien check:

```go
liened, err := h.LienStore.SumActiveDepositLiensTx(ctx, tx, accountID)
if err != nil { return err }
available := acct.CurrentBalance.Sub(liened)
if in.Amount.GreaterThan(available) {
    return httpx.E(http.StatusConflict, "withdrawal_blocked_by_lien",
        fmt.Sprintf("Withdrawal blocked: KES %s of this account is liened to loan(s) for collateral security. Available: KES %s.",
            liened.StringFixed(2), available.StringFixed(2)))
}
```

The same check joins on `bosa_liens` (Phase 5) — BOSA liens are the implicit-from-disbursement variant; the new `collateral_deposit_liens` are the explicit-from-pledge variant. Both must clear before a withdrawal succeeds. UI shows a single "Liened amount" line that's the sum of both.

**Share withdrawal / transfer check.** Same shape on `services/savings/internal/handler/shares.go` (or wherever share withdrawal lives). Reject when the pledged share count + the requested withdrawal exceeds the account's available share count.

**Pledge creation flow** for `fixed_deposit_lien` / `listed_shares` collateral kinds:

- When `POST /v1/loan-applications/{id}/collateral` is called with kind=`fixed_deposit_lien`, the body additionally requires `deposit_account_id` + `liened_amount`. The handler:
  1. Validates the account belongs to the applicant (or third-party pledger if set).
  2. Validates the account has enough available balance.
  3. Inserts `loan_collateral` with `status = 'pledged'` immediately (system-verified). Skips the `offered → verified → valued` chain.
  4. Inserts `collateral_deposit_liens` row.
  5. Audit event `loan.collateral.pledged kind=internal`.
- Same shape for `listed_shares` → `collateral_share_pledges`.
- Per the policy, internal kinds don't go through the officer-verify-then-value chain; the system itself is the verifier.

On loan settlement / write-off / collateral release:

- `collateral_deposit_liens.status` → `released`. The available balance recomputes; the member can withdraw.
- Partial release on prepayment is configurable via the existing `tenant_operations.bosa_lien_release_policy` (extend the check to also drive collateral_deposit_liens). Adapt the column name if needed; reuse the policy.

### Step 3 — Third-party pledger flow

When `loan_collateral.pledger_counterparty_id` is non-NULL on insert:

1. The handler refuses to flip status past `offered` until consent is recorded.
2. Send an SMS to the pledger (reusing the guarantor-consent SMS infrastructure spec'd earlier):
   - Token in `collateral_pledger_consent_tokens` (new table, mirrors `guarantor_consent_tokens`).
   - SMS body templated: "Hi {pledger_name}, {applicant_name} has asked you to pledge your {asset_description} as security for a {product} loan of KES {amount}. To respond: {link} or reply OFFLINE to consent in person."
   - Public route `/g/pledger/{token}` (same URL prefix as guarantor consent, different sub-path), with the same National ID + OTP verification flow.
   - Three buttons: **Accept** (status flips to `accepted`, `pledger_consent_at` set), **Decline**, **I'll bring a signed document** (admin notified to manually upload signed consent doc).
3. Officer in admin UI can record offline consent by uploading the signed doc → `pledger_consent_status = 'offline_consented'`, `pledger_consent_doc_path` set.
4. Pledger sees the pledge on their Member 360 → "Pledges given" tab (new tab on the existing member detail page).

Permission: `loans:apply` to insert third-party pledged collateral; the pledger acts via the public consent route (no permission needed; OTP gates).

### Step 4 — Charge + insurance + custody UI integration

Extend the Phase 1.5a Collateral tab + slide-over detail panel with:

**Charge Registration sub-tab** on each item that requires charge per tenant policy:

- Form: registry (Lands Registry / NTSA / Stockbroker / KRA / Other), reference number, registration date, attach certificate PDF.
- Status banner: when `charge_registry IS NOT NULL` but `charge_registered_at IS NULL`, show "Charge registration pending" amber. When all kind-required charge fields populated, show green.
- The approval gate (Phase 1.5a Step 4) now ALSO blocks approval when the item's kind requires charge per `tenant_operations.collateral_charge_required_kinds` and the registration is not yet recorded. Same 409 pattern, different message.
- On release, "Discharge filing" sub-action records `charge_discharge_ref` and `charge_discharged_at`.

**Insurance sub-tab**:

- Form: provider, policy_no, effective_from, effective_to, sum_insured, premium_amount, attach policy doc.
- Status banner: similar — pending / current / expiring-soon (≤ `collateral_insurance_warning_days`) / expired.
- New policy creation supersedes the prior current; preserves history.
- Approval gate ALSO blocks when kind requires insurance per `tenant_operations.collateral_insurance_required_kinds` and no current active policy exists.

**Document Custody sub-tab**:

- Timeline of every check-in / check-out / return.
- Action: "Check out" (officer takes the doc; captures destination + reason), "Check in" (returns to vault; captures location_code), "Return to borrower" (terminal — on release).
- Each movement requires a borrower signature on a printed receipt; signature path is uploaded as part of the movement.

**Auction sub-tab** (visible only when status = `auctioned`):

- Timeline of `collateral_auction_events`.
- Action: "Record auction event" → opens modal to add a new event row with appropriate fields per `event_kind`.
- On `proceeds_received` event, allocate the proceeds to the loan via the existing repayment endpoint, marking the channel as `auction_proceeds`. JEs land normally.

### Step 5 — Reminder workers

Two workers, both written with the same pattern as the existing `posting-dispatcher` (heartbeat, idempotent, configurable interval).

**`services/savings/cmd/collateral-revaluation-reminder`** — daily at 03:00 UTC per tenant:

1. Query `collateral_valuations WHERE is_current = true AND expires_at IS NOT NULL AND expires_at <= CURRENT_DATE + collateral_revaluation_warning_days`.
2. For each, fire an email/SMS to the assigned credit officer: "Revaluation due in N days for {kind} on loan {loan_no} ({member_name})."
3. Insert `loan_collateral_events kind='revalued_due'` so the loan detail page can flag it.
4. Idempotent — only one reminder per item per 30 days (don't spam).

**`services/savings/cmd/collateral-insurance-reminder`** — daily at 03:15 UTC per tenant:

1. Query `collateral_insurance_policies WHERE is_current = true AND status = 'active' AND effective_to <= CURRENT_DATE + collateral_insurance_warning_days`.
2. Email/SMS to officer + member: "Insurance for {asset_description} expires in N days. Provide renewed policy to avoid escalation."
3. Insert collateral event.
4. If `effective_to < CURRENT_DATE`, flip the policy to `expired` status. The collateral item retains its lifecycle status but the slide-over shows a red "Insurance expired" banner.

### Step 6 — Reports

Five new tabs under `/loans/reports`:

| Tab hash | What it shows |
| --- | --- |
| `#collateral-exposure` | Per loan: outstanding vs total approved security (FSV sum) vs guarantor coverage. Identifies under-secured loans. Sortable by shortfall. CSV export. |
| `#collateral-by-kind` | Distribution of security across portfolio. Donut chart by kind, count + total FSV per kind. |
| `#valuations-expiring` | Items with revaluation due in next 90 days. Drillable to loan detail. |
| `#insurance-expiring` | Items with insurance expiring in next 30 days. Drillable. |
| `#charge-registration-status` | Items pledged but charge not yet registered. Operational risk; cleanup queue. |

All five reuse the Phase 2 reports infrastructure (date filters, CSV export, hash-routed deep links).

Plus update the existing Phase 2 `#top-n` to optionally include a "by collateral kind" filter.

### Step 7 — Tests + observability

Go:

- `collateral_deposit_lien_test.go` — pledge creates lien; withdrawal blocked; release unblocks.
- `collateral_share_pledge_test.go` — same for shares.
- `third_party_pledger_consent_test.go` — SMS round-trip; consent flow; OTP verification.
- `charge_registration_gate_test.go` — approval gate blocks when charge required but missing.
- `insurance_gate_test.go` — same for insurance.
- `collateral_revaluation_reminder_test.go` — reminder fires once per 30 days.
- `collateral_auction_flow_test.go` — full handover → notice → sale → proceeds → loan repayment.

React:

- `CollateralCharge.test.tsx`, `CollateralInsurance.test.tsx`, `CollateralCustody.test.tsx`, `CollateralAuction.test.tsx` — per sub-tab.
- `MemberPledgesTab.test.tsx` — the new "Pledges given" tab on Member 360.
- `ReportsCollateral.test.tsx` — each of the 5 new reports.

Audit / observability:

- Event keys: `loan.collateral.lien_placed/released/exercised`, `pledger_consent_sms_sent/accepted/declined/offline`, `charge_registered/discharged`, `insurance_recorded/expiring/expired`, `custody_checked_out/in/returned`, `auction_event_recorded`.
- Metrics: `collateral_liens_active{kind}`, `collateral_insurance_expiring_total{days_to_expiry_bucket}`, `collateral_revaluations_expiring_total`, `collateral_third_party_consents{status}`.

## Acceptance walkthrough

1. **Internal deposit lien.** Member with a KES 100,000 fixed deposit. Apply for a loan; in Collateral tab pick "Fixed deposit lien" → modal lists the member's deposit accounts → pick the fixed deposit → liened_amount = 80,000. Status jumps directly to `Pledged`. The deposit account's "Available balance" on Member 360 reduces by 80,000. Try to withdraw 30,000 → succeeds (within 20,000 available). Try to withdraw 50,000 → 409 with the lien message.
2. **Third-party pledger.** Member B asks Member A to pledge their title deed for B's loan. Officer enters the collateral with `pledger_counterparty_id = A`. A receives an SMS with the token link. A clicks → verifies National ID + OTP → reads "Pledge your Plot LR12345 as security for Member B's KES 500,000 loan?" → clicks Accept. Status moves through `offered → verified` only after A's consent. The collateral row appears on A's Member 360 → Pledges given tab.
3. **Charge registration.** Apply for a property loan (`title_deed` kind in `collateral_charge_required_kinds`). Add a title deed collateral, verify, value. Try to approve → 409 "Charge registration not recorded for title_deed". Record charge: Lands Registry, ref CL/2026/12345, registered today, attach certificate. Approve → succeeds.
4. **Insurance.** Edit the same loan's vehicle collateral. Add insurance: Britam, policy ABC/2026, effective_to = 11 months. Tab shows green "Insured until …". Wait 11 months (or simulate by editing effective_to). Reminder worker fires; email to officer + member. After expiry the policy flips to `expired` and the collateral item shows the red banner.
5. **Custody.** On a title deed item, click "Check out" → enter destination "HQ Vault 2A", custodian = current user. Timeline records the movement. Member walks in to do a partial repayment that requires inspecting the title → "Check out" → enter destination = "Branch teller (Nairobi)" with a borrower signature. After verification, "Check in" → returns to vault. All three movements appear in the timeline.
6. **Auction.** Loan defaults; officer changes one collateral item to `auctioned` status. Auction sub-tab activates. Add events: handover to auctioneer (date + auctioneer name), notice published (date + notice doc), auction held (date), sold (amount = 280,000, buyer details), proceeds received (280,000 → triggers a repayment with channel `auction_proceeds`). Loan's outstanding reduces; JE lands.
7. **Reports.** Open `/loans/reports#collateral-exposure`. Top-10 most under-secured loans. Drill into one → loan detail; coverage card shows the gap. Switch to `#valuations-expiring` → list of items with valuation expiring in next 90 days. Switch to `#charge-registration-status` → items pledged but no charge registered. Each report has CSV export.

## Idempotency / safety

- Internal lien rows enforce one lien per collateral item via UNIQUE constraint. Re-disbursement / re-pledge is safe.
- Withdrawal check is read-then-block atomic inside the existing tx. Race condition between two concurrent withdrawals is handled because the COUNT-against-lien query uses FOR UPDATE on the deposit_accounts row.
- Third-party pledger SMS token expires after 7 days (reuses guarantor consent expiry). Resend creates a fresh token.
- Charge registration data is editable until the loan is settled (typos happen); audit log captures every edit.
- Insurance and revaluation reminder workers heartbeat to `worker_heartbeats` per the system health pattern.
- Auction proceeds posting goes through the existing workflow + GL pipeline; channel `auction_proceeds` is added to the channel enum.
- All new tables are RLS-scoped by `tenant_id`.
- `gofmt`, `go vet`, full `go test`, `pnpm test`, `pnpm build` all green.

When you're done, paste into the PR description: a screenshot of (a) the third-party pledger consent SMS journey end-to-end, (b) a member's "Pledges given" tab showing what they have at risk, (c) the Custody timeline on a collateral item with at least 3 movements, (d) the `#collateral-exposure` report with one under-secured loan visible, and the diff stat.