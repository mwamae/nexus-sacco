# Loan Collateral — Benchmark & Recommendation

**For Mike. No prompt yet. This is the strategic document we discuss; once you accept the recommendation, the implementation prompt follows.**

---

## 1. What's in the codebase today

I checked: the database model is well-designed but **the UI tab is empty and there's no way to add collateral after the initial application submission**.

- **Table `loan_collateral`** (`services/savings/internal/db/migrations/0005_lending.up.sql:295`): id, application_id, loan_id, **kind** (enum: `title_deed | vehicle_logbook | equipment | listed_shares | fixed_deposit_lien | other`), description, **estimated_value**, **forced_sale_value**, **valuation_date**, valuation_path, ownership_path, status (`pledged | released | auctioned`), notes.
- **API** (`services/savings/internal/handler/loan_application.go:92, 99`): the application-create endpoint accepts a `collateral: []` array in its body. That's the only write path. There is no `POST /v1/loan-applications/{id}/collateral`, no `PATCH /collateral/{id}`, no valuation workflow, no release workflow.
- **UI**: nothing. `ApplicationDetail.tsx` doesn't even reference the word "collateral".

So a SACCO using nexusSacco today can pass collateral data through the API at apply time only, with no surface to verify, value, register, revalue, release, or auction it. For a loans module that targets banks/MFIs that's a big hole.

---

## 2. Benchmark — how SACCOs, MFIs, and banks handle collateral

### 2.1 What kinds of collateral matter

Across the Kenyan landscape (SACCOs + microfinance + banks), collateral is typically segmented:

| Tier | Examples | Who uses it | Capture complexity |
| --- | --- | --- | --- |
| **Internal security** | Member shares + BOSA + FOSA pledged | All SACCOs | System auto-captures |
| **Peer security** | Guarantors (other members) | SACCOs especially | Existing flow |
| **Liquid collateral** | Fixed deposit lien (own or third-party), listed shares pledged with custodian | Banks, large SACCOs | System-verifiable |
| **Movable assets** | Vehicle logbooks (NTSA), equipment, livestock, inventory | MFIs + banks for SME | Officer-captured + valued |
| **Immovable assets** | Title deeds (Land Registry), buildings | Banks + larger SACCOs (development loans) | Officer-captured + valued + charge registered |
| **Other** | Insurance policies pledged, assignment of receivables, professional bonds | Banks for SME / commercial | Specialist; rare for SACCOs |

The data model already covers the kinds. What's missing is the **lifecycle** that turns "a row exists" into "the SACCO actually has enforceable security."

### 2.2 Lifecycle of a collateral item — what banks do

```
   Offered → Verified → Valued → Documents collected → Charge registered →
   Insurance confirmed → Approved as security → Released (on settlement)
                                              OR
                                              Auctioned (on default)
```

Each transition has named actors and required artefacts:

1. **Offered** — borrower says "I'll pledge X." Schema: row exists with `status='offered'`, `estimated_value` from borrower's claim.
2. **Verified** — officer physically inspects, confirms ownership (logbook in borrower's name, title deed search certificate), captures details. Schema: `status='verified'`, `ownership_path` populated with photos / search certs.
3. **Valued** — assigned to a panel valuer; valuer submits report with **market value** + **forced sale value**. Schema: `valuation_date`, `valuation_path`, `forced_sale_value` (the bank lends against FSV, not market value — typical 70-80% LTV against FSV).
4. **Documents collected** — original title / logbook / share certificate physically received and held in custody. Schema: new `document_custody` table tracking signed-in / signed-out chain.
5. **Charge registered** — for titles, a charge filed at the Lands Registry; for vehicles, NTSA logbook updated. Schema: new fields `charge_reference`, `charge_registered_at`, `charge_registry`.
6. **Insurance confirmed** — collateral asset insured with the SACCO as loss payee. Schema: new fields `insurance_policy_no`, `insurance_provider`, `insurance_expires_at`. Automatic reminders 30 days before expiry.
7. **Approved as security** — only verified + valued + insured collateral can satisfy the loan's collateral requirement. Schema: a per-application aggregate "Total approved security value" rolled up from FSV × LTV cap per asset.
8. **Released** — on full settlement: charge discharged, documents returned, signed receipt from borrower. Schema: `status='released'`, `released_at`, `release_receipt_path`.
9. **Auctioned** — on default, after legal demand: documents handed to auctioneer panel, sale proceeds posted against loan. Schema: new `collateral_auction_events` table.

**Banks run all nine steps formally**. SACCOs typically condense this to steps 1, 2, 3, 6, 8 — with 4, 5, 7, 9 being looser. Microfinance institutions often only do 1 and 2 for movable collateral.

### 2.3 Who actually adds collateral (across the industry)

Three patterns, in increasing sophistication:

1. **Officer-only capture** — applicant attends the branch with physical documents; officer enters everything. Simplest, but slow. Common in legacy SACCOs.
2. **Two-stage — applicant proposes, officer verifies** — applicant lists what they're offering as collateral (description, claimed value) at application time. Officer then physically inspects, takes photos, attaches valuation. The "proposed → verified" transition is the audit boundary. Common in modern banks (HF Group's HF Whizz, Equity Mobile, KCB M-PESA).
3. **Hybrid by kind** — internal-to-SACCO collateral (fixed deposit, shares pledged) is applicant-self-service because the system verifies the position exists. Physical collateral routes through officer + valuer. This is the **best fit for nexusSacco** given the existing multi-channel surface.

### 2.4 The "third-party collateral" question

Common in SACCOs: a member doesn't have enough security themselves, so a relative or fellow member offers their fixed deposit / shares as third-party security. This isn't a guarantor (a guarantor is on the hook for repayment); it's a pledge of someone else's asset.

The current schema doesn't model the third-party pledger. Banks track this as `collateral.pledger_member_id` (nullable; NULL = self-pledge) and require the third party to sign a separate pledge form. **Worth adding** for SACCO use.

### 2.5 Coverage ratios

Banks enforce a minimum "security cover" — e.g. for a KES 1M loan, total approved FSV must be ≥ KES 1.25M (125% cover). SACCOs are often laxer because guarantor coverage offsets collateral coverage. Worth being configurable per-product so a tenant can run their own policy.

---

## 3. Gap summary against the current schema

The existing `loan_collateral` table covers the **basic fields** for any asset but **misses lifecycle**. Here's what to add:

| Gap | Why it matters | Add |
| --- | --- | --- |
| **No "offered" state** — schema only has `pledged/released/auctioned` | Can't track items proposed but not yet verified by officer | Extend status enum: `offered → verified → pledged → released/auctioned` |
| **No officer verification step** | No audit trail of who physically inspected | Add `verified_by`, `verified_at`, `verification_notes` |
| **No valuer assignment workflow** | Valuation is a single field; can't track requested → returned → expiry | New `collateral_valuations` table |
| **No insurance tracking** | Banks require collateral insured; can't enforce | Add insurance fields + expiry reminders |
| **No charge registration** | Can't track Lands Registry / NTSA filing | Add `charge_reference`, `charge_registered_at`, `charge_registry` enum |
| **No document custody** | Original docs disappear with no audit | New `collateral_document_custody` table (signed in / out per movement) |
| **No third-party pledger** | Can't model relative's FD pledged as security | Add `pledger_counterparty_id` nullable |
| **No system-verified self-pledges** | Fixed deposit lien is just a description; no link to actual deposit_accounts row | Add `pledged_deposit_account_id`, `pledged_amount` (creates a real lien) |
| **No coverage ratio enforcement** | Schema can't tell you if a loan is properly secured | Add `loan_products.min_security_cover_pct` + aggregate computation |
| **No revaluation lifecycle** | Asset values change; no reminders | Add `valuation_expires_at`, reminder job |
| **No auction workflow** | Sale proceeds aren't tied back to the loan | New `collateral_auction_events` table + recovery JE flow |
| **No UI** | The whole tab is empty | The bulk of the work |

---

## 4. Recommendation

### 4.1 Adoption model — hybrid, applicant-proposes + officer-verifies

Take pattern 3 from §2.3. Specifically:

**Applicant-side** (admin UI at the application-detail Collateral tab + the future member portal):

- For **listed_shares** and **fixed_deposit_lien** kinds where the SACCO holds the asset itself, applicant clicks "Pledge fixed deposit" → modal lists their own deposit accounts (or third-party with that member's consent), they pick one + the amount to lien. System creates the lien row directly. Status jumps to `pledged` because the system verified.
- For **title_deed**, **vehicle_logbook**, **equipment**, **other** physical kinds, applicant fills a short form (kind, description, estimated value, optional photo of the document for officer reference). Row created with status=`offered`. SMS to the applicant: "We've noted your offer. Bring originals to {branch}." Notification to the assigned officer.

**Officer-side** (admin UI at the Collateral tab):

- Sees the queue of `offered` rows. Opens one → record verification (notes + photos), assign a valuer from panel, attach valuation report when it arrives, capture insurance policy details, register the charge (with reference + date).
- "Approve as security" action gates on: verified + valued + (insurance present OR exempt) + charge registered (for charge-required kinds).
- On loan settlement, "Release" wizard generates the release letter, returns documents, signs the custody log.

### 4.2 Decision gate: security policy per product — guarantors OR collateral OR both

**SACCO security is substitutable.** A member chooses guarantors OR collateral OR both, depending on the product. Each loan product defines which model applies.

New per-product enum `loan_products.security_model`:

| Value | Meaning | Typical product |
| --- | --- | --- |
| `none` | No external security; loan covered by member's shares × multiplier only | Small emergency loan, salary advance |
| `guarantor_only` | Must be fully covered by guarantor pledges | Personal loan, school fees |
| `collateral_only` | Must be fully covered by collateral FSV | Asset finance, property loan |
| `either` | Member chooses — guarantor coverage OR collateral coverage satisfies policy | Flexible development loan |
| `both` | Both required at their respective minimums | High-value commercial loan, plot purchase |

With per-product thresholds:

- `min_guarantor_cover_pct` (default 100 — every shilling of loan covered by guarantor pledges)
- `min_collateral_cover_pct` (default 125 — collateral FSV is 125% of loan amount)

**The approval gate computes both coverages and applies the model's rule.** The workflow's approve action returns `409 Conflict` with a clear message when the policy isn't met. Examples:

- `guarantor_only` product, KES 100,000 loan, accepted guarantor pledges total KES 80,000 → fail: "Guarantor coverage is 80% of required 100%. Add KES 20,000 in guarantor pledges or change product to one accepting collateral."
- `either` product, KES 100,000 loan, KES 60,000 guarantor + KES 60,000 collateral FSV → pass on collateral side (60% guarantor short but 75% on collateral path — wait, both shortfall…). Actually: `either` passes if EITHER side independently meets its threshold (60% < 100% AND 60% < 125%, so both fail individually → policy not met). The member must boost one side, not split-cover.
- `both` product, KES 100,000 loan, KES 100,000 guarantor + KES 100,000 collateral FSV → fail: "Collateral coverage is 100% of required 125%. Add KES 25,000 collateral FSV."

The application detail page shows a Security Coverage card with **both** totals side-by-side and explicitly names which threshold(s) are being checked under the active policy.

This is enforced at workflow approve-action time, not just UI-side. Storage:

```sql
ALTER TABLE loan_products
  ADD COLUMN security_model text NOT NULL DEFAULT 'guarantor_only'
    CHECK (security_model IN ('none','guarantor_only','collateral_only','either','both')),
  ADD COLUMN min_guarantor_cover_pct numeric(5,2) NOT NULL DEFAULT 100,
  ADD COLUMN min_collateral_cover_pct numeric(5,2) NOT NULL DEFAULT 125;
```

Drops the original §4.2 collateral-only check. Existing products keep working — they default to `guarantor_only` (the SACCO norm), and the existing per-loan multiplier check still applies orthogonally to whatever the security_model says.

### 4.3 Internal security (deposit lien + share pledge) as first-class

The schema has `fixed_deposit_lien` as a `kind` but no link to the actual deposit account being liened. This is exactly the same problem the prior BOSA-lien work solved with `bosa_liens` (Phase 5, decision 9.2). Reuse the pattern:

- New `collateral_deposit_liens` table linking a `loan_collateral` row to a specific `deposit_accounts.id` + amount.
- When `kind='fixed_deposit_lien'` and the lien is `pledged`, the deposit account is constrained — withdrawals refuse if they'd drop the available balance below the liened amount.
- Same for `listed_shares` if the SACCO holds the shares — link to `share_accounts`.

This integrates the collateral lifecycle with the savings module's existing constraint engine. Auditor-friendly because the lien is enforceable, not just descriptive.

### 4.4 Third-party pledger support

Add `pledger_counterparty_id uuid REFERENCES counterparties(id)` nullable. NULL = self-pledge. When non-NULL:

- That counterparty must be a member (validated at insert).
- They receive an SMS consent request similar to the guarantor consent SMS we just spec'd — tokenised link, accept/decline, record signature path.
- Status doesn't go past `offered` until the pledger consents.
- Pledger sees the pledge on their Member 360 → "Pledges given" tab (so a member can see what assets of theirs are tied to whose loans).

### 4.5 Settings — per-tenant defaults + per-product policy

Security model and thresholds are per-product (§4.2). Tenant-level settings exist as **defaults** the product editor inherits when a new product is created, and to control the operational features that apply to all collateral regardless of which product needs it:

- `default_security_model` enum — used as the New Product form default. Defaults to `guarantor_only` for SACCO use.
- `default_min_guarantor_cover_pct` — default 100.
- `default_min_collateral_cover_pct` — default 125.
- `collateral_revaluation_months int` — how often revaluation is required; default 24. Per-kind overridable.
- `collateral_charge_required_kinds text[]` — which kinds require formal charge registration before approval. Default `['title_deed', 'vehicle_logbook']`.
- `collateral_insurance_required_kinds text[]` — which kinds require insurance. Default `['title_deed', 'vehicle_logbook', 'equipment']`.

### 4.6 UI shape — what the Collateral tab becomes

On `/loans/applications/{id}` and `/loans/register/{id}` (the loan detail), the Collateral tab gets:

**Header card — Security coverage (policy-aware)**

The card surfaces both coverage sources and the active product policy. Example for an `either` product, KES 1,000,000 loan, with both sources contributing:

```
┌─────────────────────────────────────────────────────────────────────────┐
│  Security policy: EITHER (guarantor 100% OR collateral 125%)              │
│                                                                          │
│  Guarantor coverage:   142%   ✓  KES 1,420,000 pledged                    │
│  Collateral coverage:    0%   —  no collateral on file                    │
│                                                                          │
│  Status: ✓ MEETS POLICY  (passes on guarantor side)                       │
└─────────────────────────────────────────────────────────────────────────┘
```

For a `both` product example showing a partial fail:

```
┌─────────────────────────────────────────────────────────────────────────┐
│  Security policy: BOTH (guarantor 100% AND collateral 125%)               │
│                                                                          │
│  Guarantor coverage:   100%   ✓  KES 500,000 pledged                      │
│  Collateral coverage:   80%   ✗  KES 400,000 of required KES 625,000      │
│                                                                          │
│  Status: ✗ POLICY NOT MET — Add KES 225,000 collateral FSV                │
└─────────────────────────────────────────────────────────────────────────┘
```

For `guarantor_only` and `collateral_only` products, the irrelevant row is shown muted with "n/a for this product" so a glancing officer knows which sources count.

**Action bar**

- "Add collateral" → dropdown of kinds. Internal kinds (deposit lien, share pledge) open a system-verified modal. Physical kinds open the form for proposed entry.
- "Audit log" → drawer showing every transition on every item.

**Items list**

Per row: kind (icon + label), description, status pill (Offered / Verified / Valued / Pledged / Released / Auctioned), estimated value, FSV, insurance status (green/amber if expiring/red if expired), action buttons appropriate to the status.

**Item detail panel** (slide-over):

- Photos + documents
- Timeline (Offered by X on date → Verified by Y on date → Valued by valuer Z on date → Charge registered ref Q on date → Pledged → Released)
- Valuation history (because revaluation creates new valuation records)
- Insurance history
- Custody log (each time the original document was checked out / in)
- Actions appropriate to current state (Verify / Assign valuer / Attach valuation / Record insurance / Register charge / Approve as security / Release / Auction)

### 4.7 Reporting additions

- `/loans/reports#collateral-exposure` — per loan: total security held vs outstanding. Identifies under-secured loans.
- `/loans/reports#collateral-by-kind` — distribution of security across portfolio.
- `/loans/reports#valuations-expiring` — revaluations due in next 90 days.
- `/loans/reports#insurance-expiring` — collateral insurance expiring in next 30 days.
- `/loans/reports#charge-registration-status` — items pledged but charge not yet registered (operational risk).

These integrate cleanly into the Phase 2 reports tabs.

### 4.8 Where this fits in the phasing

The user just approved Phase 1 (consolidated UI). Phase 1's Collateral tab can ship as a placeholder ("Collateral management — see follow-up PR") OR with the read-only display of existing rows. The full collateral work is best shipped as **Phase 1.5 — Collateral lifecycle**, between Phase 1 and Phase 2. Rationale:

- It's small enough to be one PR (the schema additions are tightly scoped).
- It's not on the critical path for reporting (Phase 2 doesn't depend on it).
- Shipping it after Phase 1 means the collateral tab launches with real functionality rather than a "coming soon" message.

Alternative: ship Phase 1 as-is with the placeholder, then Phase 1.5 inserts collateral work. Either order works.

### 4.9 What I'd defer to Phase 6

- **Valuer panel marketplace** (auto-dispatch to multiple valuers, competitive quoting) — over-engineered for SACCO use.
- **Auction integration** (auctioneer panel, sale proceeds via M-PESA, public auction notice generation) — small set of defaulted loans; manual workflow is fine for a long time.
- **Lands Registry / NTSA direct integration** for automatic charge registration — vendor-dependent, regulatory complexity.

---

## 5. What I need from you

1. **Adoption model**: §4.1 hybrid (applicant-proposes + officer-verifies, system-verifies internal). Agree?
2. **Approval gate**: §4.2 — per-product `security_model` enum (`none / guarantor_only / collateral_only / either / both`) with per-product guarantor and collateral cover thresholds. Hard gate at approval. Agree?
3. **Internal-security linkage**: §4.3 — promote `fixed_deposit_lien` and `listed_shares` kinds to a first-class lien on the deposit/share account. Same shape as BOSA liens. Agree?
4. **Third-party pledger**: §4.4 — add support, with SMS consent flow. Agree?
5. **Per-tenant defaults**: §4.5 — defaults for the New Product form + tenant-wide collateral operational settings. Anything missing?
6. **Phasing**: §4.8 — Phase 1.5 between Phase 1 and Phase 2, or different placement?
7. **Deferred items**: §4.9 — confirm valuer panel + auction integration + registry integration are out of scope for now.

Once you reply with picks, I'll write the implementation prompt.