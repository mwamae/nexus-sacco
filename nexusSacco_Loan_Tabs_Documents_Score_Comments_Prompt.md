# Loan detail — wire up Documents, Score, Comments tabs

Single PR. Fix the three remaining placeholder tabs on the loan application + loan detail pages:

- **Documents** — currently a placeholder div. Wire upload/list/download, per-product required-docs checklist, expiry tracking, per-document review state, versioning, multiple instances per kind, and a single-PDF bundle export.
- **Score** — currently dumps raw JSON. Replace with a structured presentation: header card, affordability waterfall, multiplier check, hard blocks vs advisories, per-factor breakdown, score history with timeline, re-score action. Drop the raw-JSON developer view entirely.
- **Comments** — currently a placeholder div. Build the hybrid (`internal` + `external`) `loan_comments` table with threading, pinning, attachments, edit history, member SMS firing for external posts, free-text search, and a small starter set of templates.

Picks locked in:

- Required-docs checklist driven by per-product config; expiry windows from tenant settings; PDF bundle (not ZIP); review state per document; versioning.
- Score presentation drops the raw-JSON toggle entirely.
- Comments use the hybrid model (Option B from the review).
- Any officer with `loans:apply` can post external comments.
- Single PR for all three tabs.

## Files to read first

- `web/admin/src/pages/Loans/Applications/LoanApplicationDetail.tsx` lines 46–53 (tab definitions), 241 (active-tab switch), 529–619 (the three placeholder components).
- `web/admin/src/pages/Loans/LoanDetail.tsx` — same shape for post-disbursement view; mirror everything.
- `services/savings/internal/db/migrations/0005_lending.up.sql:316` — existing `loan_documents` table + `loan_doc_kind` enum.
- `services/savings/internal/handler/guarantor_consent.go:127–148` and `services/savings/internal/handler/loan_collections_events.go:680–695` — existing inline `INSERT INTO loan_documents` patterns. Both should migrate to the new store after this PR; do the migration in this PR.
- `services/savings/internal/handler/loan_application.go:6, 360` — the existing `Re-score` endpoint that the new score header card invokes.
- `services/savings/internal/db/migrations/0005_lending.up.sql:227–239` — the `loan_applications` columns the score tab presents (`credit_score, risk_band, affordability_pass, dti_ratio, net_disposable_income, computed_max_amount, computed_max_installment, recommended_amount, recommended_term_months, scoring_details, scoring_flags, scored_at`).
- `services/member/internal/handler/member.go::UploadDocument`, `DownloadDocument` — the template for multipart upload with audit + storage backend integration.
- `services/notification/internal/pdf/` — the PDF generation infrastructure used by collections letters. We extend it for PDF-bundle export.
- `services/notification/internal/sms/sender.go` — the SMS sender used for OTP and collections; reused for external comment notifications.

## Scope — one PR, eight steps

### Step 1 — Schema migrations

New migration `services/savings/internal/db/migrations/00NN_loan_documents_score_history_comments.up.sql`:

```sql
BEGIN;

-- ── 1. Documents: required-kinds config, expiry, review state, versioning ──

ALTER TABLE loan_products
  ADD COLUMN IF NOT EXISTS required_document_kinds text[] NOT NULL DEFAULT ARRAY[]::text[];
COMMENT ON COLUMN loan_products.required_document_kinds IS
  'Array of loan_doc_kind enum values (as text). Workflow approve gate refuses to advance the application until each named kind has at least one current document attached.';

-- Per-tenant defaults the New Product form inherits, plus the per-kind expiry policy.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS default_required_document_kinds text[] NOT NULL DEFAULT ARRAY['id_copy','payslip','bank_statement']::text[],
  ADD COLUMN IF NOT EXISTS document_expiry_windows jsonb NOT NULL DEFAULT '{
    "id_copy":             1825,
    "payslip":             60,
    "bank_statement":      90,
    "mpesa_statement":     90,
    "business_financials": 365,
    "kra_pin_certificate": 365,
    "valuation_report":    730
  }'::jsonb,
  ADD COLUMN IF NOT EXISTS document_expiry_warning_days int NOT NULL DEFAULT 14;

ALTER TABLE loan_documents
  ADD COLUMN IF NOT EXISTS expires_at        date,
  ADD COLUMN IF NOT EXISTS review_status     text NOT NULL DEFAULT 'pending'
    CHECK (review_status IN ('pending','reviewed','needs_replacement','flagged')),
  ADD COLUMN IF NOT EXISTS reviewed_by       uuid,
  ADD COLUMN IF NOT EXISTS reviewed_at       timestamptz,
  ADD COLUMN IF NOT EXISTS review_notes      text,
  ADD COLUMN IF NOT EXISTS is_current        boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS superseded_by_id  uuid REFERENCES loan_documents(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS loan_documents_app_current_idx
  ON loan_documents (application_id, kind) WHERE is_current = true;
CREATE INDEX IF NOT EXISTS loan_documents_loan_current_idx
  ON loan_documents (loan_id, kind) WHERE is_current = true;
CREATE INDEX IF NOT EXISTS loan_documents_expiring_idx
  ON loan_documents (tenant_id, expires_at)
  WHERE is_current = true AND expires_at IS NOT NULL;

-- ── 2. Score history (append-only) ──

CREATE TABLE loan_application_score_history (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id               uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id          uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  scored_at               timestamptz NOT NULL DEFAULT now(),
  scored_by               uuid,
  credit_score            int,
  risk_band               text,
  affordability_pass      boolean,
  dti_ratio               numeric(6,3),
  net_disposable_income   numeric(18,2),
  computed_max_amount     numeric(18,2),
  computed_max_installment numeric(18,2),
  recommended_amount      numeric(18,2),
  recommended_term_months int,
  scoring_details         jsonb,
  scoring_flags           jsonb,
  trigger_reason          text                                  -- 'initial_score' | 'manual_rescore' | 'guarantor_changed' | 'document_added'
);
CREATE INDEX loan_app_score_history_app_idx
  ON loan_application_score_history (application_id, scored_at DESC);
ALTER TABLE loan_application_score_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_app_score_history ON loan_application_score_history
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- ── 3. Comments (hybrid internal + external) ──

CREATE TABLE loan_comments (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id      uuid REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id             uuid REFERENCES loans(id) ON DELETE CASCADE,
  parent_id           uuid REFERENCES loan_comments(id) ON DELETE CASCADE,
  visibility          text NOT NULL CHECK (visibility IN ('internal','external')),
  body                text NOT NULL CHECK (length(body) > 0),
  attachment_paths    jsonb NOT NULL DEFAULT '[]'::jsonb,
  author_user_id      uuid,                                     -- NULL when from member SMS reply
  author_member_id    uuid,                                     -- NULL when from officer
  posted_at           timestamptz NOT NULL DEFAULT now(),
  edited_at           timestamptz,
  edit_history        jsonb NOT NULL DEFAULT '[]'::jsonb,
  pinned              boolean NOT NULL DEFAULT false,
  member_read_at      timestamptz,
  -- For external SMS reply pairing — outbound SMS carries this token so an inbound SMS reply attaches to the right thread.
  reply_token         uuid UNIQUE,
  CHECK ((application_id IS NOT NULL) <> (loan_id IS NOT NULL)),
  CHECK ((author_user_id IS NOT NULL) <> (author_member_id IS NOT NULL))
);
CREATE INDEX loan_comments_app_idx ON loan_comments (application_id, posted_at DESC)
  WHERE application_id IS NOT NULL;
CREATE INDEX loan_comments_loan_idx ON loan_comments (loan_id, posted_at DESC)
  WHERE loan_id IS NOT NULL;
CREATE INDEX loan_comments_pinned_idx ON loan_comments (tenant_id, pinned)
  WHERE pinned = true;
CREATE INDEX loan_comments_external_unread_idx ON loan_comments (tenant_id, posted_at DESC)
  WHERE visibility = 'external' AND member_read_at IS NULL;
ALTER TABLE loan_comments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_comments ON loan_comments
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

CREATE TABLE loan_comment_templates (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  label               text NOT NULL,
  visibility          text NOT NULL CHECK (visibility IN ('internal','external')),
  body                text NOT NULL,
  is_active           boolean NOT NULL DEFAULT true,
  created_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, label)
);
ALTER TABLE loan_comment_templates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_comment_templates ON loan_comment_templates
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

-- Seed default external templates per tenant.
INSERT INTO loan_comment_templates (tenant_id, label, visibility, body)
SELECT t.id, x.label, 'external', x.body
  FROM tenants t
  CROSS JOIN (VALUES
    ('Documents needed',
     'Hi {member_name}, we need the following documents to continue your application: {missing_documents}. Please upload them at your earliest convenience.'),
    ('Approval pending',
     'Hi {member_name}, your application is under review by our credit committee. We will update you within 48 hours.'),
    ('Disbursement scheduled',
     'Hi {member_name}, your loan has been approved. Disbursement is scheduled for {disbursement_date} via {channel}.'),
    ('Repayment due',
     'Hi {member_name}, a friendly reminder that your loan installment of KES {installment_amount} is due on {due_date}.')
  ) AS x(label, body)
ON CONFLICT (tenant_id, label) DO NOTHING;

-- A small set of internal templates as well.
INSERT INTO loan_comment_templates (tenant_id, label, visibility, body)
SELECT t.id, x.label, 'internal', x.body
  FROM tenants t
  CROSS JOIN (VALUES
    ('Employment verified', 'Spoke with employer ({contact}) — employment confirmed.'),
    ('Site visit complete',  'Conducted site visit on {date}. Findings: {findings}.'),
    ('Recommend approval',   'Recommending approval at {amount} / {term} months. Risk acceptable given {rationale}.')
  ) AS x(label, body)
ON CONFLICT (tenant_id, label) DO NOTHING;

COMMIT;
```

Down migration drops the new tables/columns in reverse dependency.

### Step 2 — Backend: documents

New file `services/savings/internal/handler/loan_documents.go`. Endpoints:

| Endpoint | Body | Permission |
| --- | --- | --- |
| `POST   /v1/loan-applications/{app_id}/documents` | multipart: `file`, `kind`, `description?`, `expires_at?` | `loans:apply` |
| `POST   /v1/loans/{loan_id}/documents` | same | `loans:apply` |
| `GET    /v1/loan-applications/{app_id}/documents?include_history=false` | list of current (or full history) | `loans:view` |
| `GET    /v1/loans/{loan_id}/documents?include_history=false` | same | `loans:view` |
| `GET    /v1/loan-documents/{id}/download` | streams the file | `loans:view` |
| `GET    /v1/loan-applications/{app_id}/documents/bundle.pdf` | streams the merged PDF | `loans:view` |
| `GET    /v1/loans/{loan_id}/documents/bundle.pdf` | same | `loans:view` |
| `POST   /v1/loan-documents/{id}/review` | `{status: 'reviewed'|'needs_replacement'|'flagged', notes?}` | `loans:apply` |
| `DELETE /v1/loan-documents/{id}` | only valid while not yet pledged-as-required | `loans:apply` |
| `GET    /v1/loan-applications/{app_id}/required-documents-status` | per-kind checklist response (Step 3) | `loans:view` |

Upload behaviour:

- Reuse the member-documents pattern from `services/member/internal/handler/member.go::UploadDocument`. Same `Storage.Save` call, same MIME allowlist, same size cap.
- Compute `expires_at` automatically: `uploaded_at + tenant_operations.document_expiry_windows[kind]` days. Allow the caller to override via the body.
- If a document of the same `(application_id_or_loan_id, kind)` already exists with `is_current=true`, mark the existing row `is_current=false` and set `superseded_by_id = new.id`. The new row becomes current.
- Insert + emit audit event `loan.document.uploaded` with metadata `{kind, supersedes_id?}`.

Refactor `services/savings/internal/handler/guarantor_consent.go` and `loan_collections_events.go` to call the new store's `Insert` method instead of inline `INSERT INTO loan_documents`. This consolidates writes through one seam (matches the pattern established in earlier consolidation PRs).

Bundle endpoint:

- Use `services/notification/internal/pdf/` (or vendor the same lib used there — likely `gofpdf` or `pdfcpu`).
- Iterate `is_current = true` documents for the application/loan in sorted order (by `kind` then `uploaded_at`).
- For PDF source files: append page-by-page.
- For image source files (jpg, png, etc.): create a new page and embed the image.
- Other MIME types (rare): write a "file omitted" placeholder page with the kind, description, and filename.
- Add a cover page listing every included document (kind, description, uploaded_at, reviewer state).
- Stream the result back; cache for 60s per `(application_or_loan_id, max(uploaded_at))` so re-clicks don't regenerate.

### Step 3 — Backend: required-documents gate

In the existing workflow approve-action for `loan_application_decision`, before allowing the approve, check:

```go
required := product.RequiredDocumentKinds  // text[]
for _, kind := range required {
    if !DocStore.HasCurrentNonExpiredTx(ctx, tx, app.ID, kind) {
        return errors.New("required document missing or expired: " + kind)
    }
}
```

`HasCurrentNonExpiredTx` is `SELECT EXISTS (SELECT 1 FROM loan_documents WHERE application_id = $1 AND kind = $2 AND is_current = true AND (expires_at IS NULL OR expires_at > CURRENT_DATE))`.

The endpoint `GET /required-documents-status` returns the structured shape the UI uses:

```json
{
  "required": ["id_copy", "payslip", "bank_statement"],
  "status": {
    "id_copy": {"satisfied": true, "current_doc_id": "...", "expires_at": "2031-..."},
    "payslip": {"satisfied": true, "current_doc_id": "...", "expires_at": "2026-07-22", "warning": "expires_in_45_days"},
    "bank_statement": {"satisfied": false, "reason": "no_document"}
  },
  "all_satisfied": false,
  "summary": "2 of 3 required documents satisfied"
}
```

The approval gate's 409 error body uses the same shape so the UI can render the missing-list directly.

### Step 4 — Backend: score history + Re-score wiring

Update the existing `ReScore` handler (`services/savings/internal/handler/loan_application.go:360`) to insert a `loan_application_score_history` row in the same tx as the score recompute. The new row carries `trigger_reason` derived from how the rescore was invoked (header `X-Score-Trigger: manual_rescore | guarantor_changed | document_added | initial_score`).

Add automatic rescoring hooks:

- Guarantor consent flips to `accepted` / `declined` → re-score with `trigger_reason='guarantor_changed'`.
- Document with kind ∈ `required_document_kinds` becomes current → re-score with `trigger_reason='document_added'`.

Both run inside the existing tx where the upstream change commits, so the score is always in sync with the inputs.

New endpoint `GET /v1/loan-applications/{app_id}/score/history` returns the score history rows sorted desc. UI builds the timeline from this.

### Step 5 — Backend: comments

New file `services/savings/internal/handler/loan_comments.go`. Endpoints:

| Endpoint | Body | Permission |
| --- | --- | --- |
| `POST   /v1/loan-applications/{app_id}/comments` | `{visibility, body, parent_id?, attachment_paths?, template_id?}` | `loans:apply` (both internal and external — confirmed pick) |
| `POST   /v1/loans/{loan_id}/comments` | same | `loans:apply` |
| `GET    /v1/loan-applications/{app_id}/comments?include_external=true` | threaded list | `loans:view` |
| `GET    /v1/loans/{loan_id}/comments?include_external=true` | same | `loans:view` |
| `PATCH  /v1/loan-comments/{id}` | `{body}` | `loans:apply` (and `author_user_id = caller`) |
| `POST   /v1/loan-comments/{id}/pin` | `{pinned: bool}` | `loans:apply` |
| `DELETE /v1/loan-comments/{id}` | soft-delete (sets body to "[deleted]" + audit) | `loans:apply` (and `author_user_id = caller`) |
| `GET    /v1/loan-comments/templates` | list active templates | `loans:view` |
| `GET    /v1/loan-comments/search?q=...&application_id=` | full-text search | `loans:view` |
| `POST   /v1/portal/comments/{token}/reply` | public (token-gated; member SMS reply lands here when inbound SMS hook fires) | none — token gates |

Comment-post behaviour:

- **Internal** comments: insert row, return.
- **External** comments: insert row with a freshly-generated `reply_token`, then fire an SMS to the member via the notification client with body:
  ```
  Hi {member_name}, {tenant_name} sent you a message about your loan: "{body_truncated_to_120_chars}". Reply via {short_url}. Ref: {token_short}
  ```
  `short_url` points at `https://{tenant}.nexussacco.local/m/c/{token}` — public route that shows the thread + lets the member reply.

Inbound SMS reply handling: if the notification service's inbound-SMS hook exists, extend it to detect SMS-reply patterns including the token reference; route the reply via `/v1/portal/comments/{token}/reply` inserting a member-authored comment with `author_member_id` set. If inbound SMS isn't wired today, skip this leg — link-based replies still work.

Member-read tracking: when the member opens the public URL, the handler updates `member_read_at` on every external comment in the thread the token belongs to. Officer UI shows a "Read N hours ago" indicator.

Edit behaviour: `PATCH` records the previous body + previous timestamp into `edit_history` JSONB, sets `edited_at`. UI shows "(edited)" with hover-history.

Search endpoint: simple ILIKE on `body`, scoped by `application_id` or `loan_id`. Phase 6 can wire proper full-text search if volume demands.

Soft-delete: rather than DELETE, the body is replaced with `[deleted]` and the row stays so threading and audit chain remain intact. Original body preserved on the audit row.

### Step 6 — Frontend: Documents tab

Rewrite `DocumentsTab` (in both `LoanApplicationDetail.tsx` and `LoanDetail.tsx`) and add a new shared component `web/admin/src/components/Loans/DocumentsTab.tsx`.

Two regions:

**Region A — Required documents checklist** (only shown when the product has required kinds):

```tsx
<RequiredDocsChecklist data={statusPayload} onUpload={(kind) => openUploadModal(kind)} />
```

Each row: ✓ / ✗ / ⚠, kind label (humanised from enum), count of current documents, latest upload date, expiry warning if relevant. ✗ rows have an inline "Upload" button that opens the upload modal pre-selected on that kind.

**Region B — All documents list**:

```
Kind            Description       Status       Expires      Uploaded    Actions
payslip         June 2026         ✓ reviewed   2026-07-22   2 days ago  [download] [history]
payslip         May 2026          ✓ reviewed   2026-06-22   1mo ago     [download] [history]
bank_statement  StanChart Q1      ⚠ pending    2026-08-15   1 day ago   [download] [review]
guarantor_proof J. Mwangi consent ✓ reviewed   —            3 days ago  [download]
mpesa_statement Apr 2026          ⚠ EXPIRED    2026-04-20   2mo ago     [download] [replace]
```

Sort by `kind` then `uploaded_at` DESC. Default view shows only `is_current=true`; "Show history" toggle includes superseded rows greyed out.

Top-right actions: **Upload document** (modal asks kind + file + optional description + optional expiry override), **Download bundle (PDF)** (calls the bundle endpoint and saves the resulting file as `Loan-{loan_no_or_app_no}-Documents.pdf`).

Upload modal:

- Kind dropdown (filtered by `loan_doc_kind` enum)
- Description text field
- Expiry date (pre-filled from product config; editable)
- File input (accept = .pdf, image/*, .doc, .docx)
- Server-side max-size check; surface the size cap in the modal copy

Review modal (opens when officer clicks "review" on a pending document):

- Document preview (inline PDF or image render)
- Three buttons: ✓ Mark reviewed / ⚠ Needs replacement / 🚩 Flag
- Notes field (optional for ✓; required for the other two)

The Documents tab on `LoanDetail.tsx` (post-disbursement) shows the same data but with additional post-disbursement kinds visible: signed offer letter, disbursement receipts, repayment proofs.

### Step 7 — Frontend: Score tab

Replace `ScoreTab` entirely in both `LoanApplicationDetail.tsx` and `LoanDetail.tsx`. New shared component `web/admin/src/components/Loans/ScoreTab.tsx`.

Layout:

**Header card**:

```
┌─ SCORING SUMMARY ─────────────────────────────────────┐
│  Score: 720   Risk: A                                  │
│  [✓ APPROVE recommended]                               │
│  KES 250,000 / 12 months @ 14%                         │
│  Last scored: 2 hours ago (manual rescore)             │
│                                       [Re-score]      │
└────────────────────────────────────────────────────────┘
```

The big number is the `credit_score` rendered with a colour ramp (green for A/Green, amber for B/Amber, red for D/Red). The verdict pill comes from a small derivation function: pass if affordability_pass && score >= product.min_score && no hard blocks; otherwise decline / advisory. Source the rationale text from the strongest flag.

**Affordability waterfall card**:

Table rendering with monospace amounts. Row classes for income (positive), deductions (negative), totals (bold). Last row is the affordability pass/fail with a green ✓ or red ✗. DTI shown next to the policy threshold.

**Multiplier check card**:

Compact card with ceiling, requested, headroom (positive or shortfall in red).

**Hard blocks** (red-bordered section, only shown if non-empty):

Each block is a row with severity icon, code (e.g. `crb_listing`, `affordability_fail`), human-readable description, suggested action. The `scoring_flags` JSONB is parsed; expected shape `[{code, severity, description, action}]`.

**Advisories** (amber-bordered section, only shown if non-empty):

Same shape as hard blocks but amber styling. The component renders nothing for the section if no advisories.

**Per-factor breakdown table**:

```
Factor                  Weight   Score    Contribution
Member tenure           20%      80       16.0
Repayment history       30%      95       28.5
Employment stability    15%      70       10.5
Income consistency      15%      75       11.25
Existing exposure       10%      80       8.0
CRB rating              10%      —        (not yet pulled)
──────────────────────────────────────────────────────
Total                                     74.25 → 720
```

Parse `scoring_details` JSONB — expected shape `{factors: [{code, label, weight_pct, score, contribution}], total_score, normalised_to}`. Factors with no score render `—` in the score column and a muted explanation in the contribution column. Factor list and weights come from the scoring engine; the tab presents what it gets.

**Re-score button**:

Confirmation modal: "Re-score this application now? This will recompute the score using the latest data (income, guarantors, documents, CRB) and create a new entry in score history. The current score will be archived."

Calls `POST /score` with `X-Score-Trigger: manual_rescore`.

**Score history timeline**:

Reads `GET /score/history`. Renders a vertical timeline:

```
2 hours ago      Manual rescore               720 (A)    [diff vs prior]
─── 1 day ago    Guarantor changed            580 (B)
─── 3 days ago   Initial score                520 (C)
```

Each row clickable to expand the full breakdown for that historical score (renders the same waterfall/multiplier/factors but for that point-in-time data).

**Empty state** (no scoring data):

Clean empty state card explaining what scoring needs (income captured? employment? guarantors?), with a "Re-score now" button. No JSON dump anywhere.

### Step 8 — Frontend: Comments tab

New shared component `web/admin/src/components/Loans/CommentsTab.tsx`.

Layout:

**Composer at the top** (visible when user has `loans:apply`):

```tsx
<CommentComposer
  onPost={(visibility, body, attachmentPaths, parentId) => …}
  templates={availableTemplates}
  defaultVisibility="internal"
/>
```

Composer features:

- Body textarea
- Visibility radio: ◯ Internal only / ◯ Send to member (external)
- Template dropdown — clicking inserts the template body; placeholders like `{member_name}` interpolated server-side at post time
- Attach button (uses the same upload infra as Documents, with attachment_paths stored on the comment)
- Post button (disabled until body is non-empty)
- Character counter when visibility=external (SMS-relevant)

**Pinned section** (only if there are pinned comments):

Renders pinned comments at the top with a 📌 icon. Pin/unpin from the action bar on each comment.

**Comment thread**:

Threaded display: top-level comments listed chronologically (oldest first or newest first — toggle in header), with replies indented under their parent.

Each comment renders:

- Avatar + author name + role chip (officer / member)
- Visibility chip (Internal / External 📤 sent / External 📥 reply)
- Timestamp + (edited) marker if applicable
- Body (markdown supported — light renderer; no images embedded inline, only attachment chips)
- Attachment chips
- Action bar: Reply / Edit (own only) / Pin / Delete (own only, soft)
- For external comments, member read status: "Member read 2 hours ago" or "Not yet read"

**Search bar** at the top:

Free-text search box. Triggers `GET /search?q=`. Results filter the thread inline.

**Empty state**:

"No comments yet. Start a thread to capture officer notes or send a message to the member."

### Step 9 — Tests + observability

Go:

- `loan_documents_test.go` — upload, list, supersede on re-upload, expires_at computation from tenant config, review state transitions, delete only when not required-and-current, gate refusing approval on missing/expired required docs.
- `loan_document_bundle_test.go` — bundle generates with PDF + image inputs + missing-format placeholder; cover page present; cache hit on second call.
- `loan_application_score_history_test.go` — manual rescore creates row; guarantor change triggers rescore; document upload triggers rescore; history endpoint sorts correctly.
- `loan_comments_test.go` — post internal, post external (asserts SMS fired), edit preserves history, pin/unpin, soft delete, template-based post interpolates placeholders.
- `loan_comments_member_reply_test.go` — public route accepts a token, inserts a member-authored comment.

React:

- `DocumentsTab.test.tsx` — required-docs checklist renders correctly; upload modal validates; bundle download triggers; review modal flow.
- `ScoreTab.test.tsx` — header card renders for pass + fail cases; affordability waterfall math; per-factor table; score history timeline.
- `CommentsTab.test.tsx` — composer toggles visibility; templates insert into body; pin/unpin; soft-delete shows [deleted].

Audit + metrics:

- Event keys: `loan.document.uploaded`, `loan.document.reviewed`, `loan.document.superseded`, `loan.document.bundle_generated`, `loan.application.scored`, `loan.comment.posted`, `loan.comment.edited`, `loan.comment.pinned`, `loan.comment.member_read`, `loan.comment.member_replied`.
- Metrics: `loan_documents_total{kind,review_status}`, `loan_documents_expired_total`, `loan_application_rescores_total{trigger}`, `loan_comments_total{visibility}`, `loan_comments_external_unread`.

## Acceptance walkthrough

1. **Required docs gate.** Edit a loan product → set Required document kinds to `id_copy, payslip, bank_statement`. Save. Member applies for a loan under that product. Open the Documents tab. Checklist shows all three as ✗. Try to approve → 409 "Required documents missing: id_copy, payslip, bank_statement". Upload an id_copy with no expires_at — auto-computed to today + 1825 days. Checklist updates to 1 of 3 ✓. Upload the other two; gate clears; approval succeeds.

2. **Document expiry.** Upload a `payslip` with `expires_at = today + 60d` (auto). Edit `tenant_operations.document_expiry_warning_days = 30`. Adjust system clock 31 days forward (or set the doc's `expires_at` to today + 13d via SQL). Checklist now shows the payslip with an amber warning "expires in 13 days". 14 days later, status flips to red EXPIRED; the gate refuses approval again until a fresh payslip is uploaded.

3. **Versioning.** Upload a new payslip — the old one supersedes. Default list shows only the new. Toggle "Show history" → old one greyed out below the new.

4. **PDF bundle.** Click "Download bundle (PDF)" on a loan with 5 documents (2 PDFs, 2 images, 1 docx). Saved file opens — cover page lists all 5, followed by the 2 PDFs page-by-page, the 2 images as full-page embeds, and a "file omitted (docx)" placeholder for the last.

5. **Score header + waterfall.** Open an application with a scored row. Score tab shows the header card (no JSON anywhere). Affordability waterfall sums correctly. Multiplier check shows headroom. Hard blocks empty, advisories show "approaching DTI ceiling — current 56%, policy 60%".

6. **Re-score + history.** Click Re-score. New row appears in `loan_application_score_history`. Tab shows the new score in the header; timeline below shows the previous score archived. Add a new guarantor — the system auto-rescores; another timeline entry appears with trigger `guarantor_changed`.

7. **Internal comment.** Compose an internal comment "Verified employer — confirmed." Post. Appears in the thread with Internal chip. Member is NOT notified.

8. **External comment.** Compose with visibility=external using the "Documents needed" template; placeholders `{member_name}` interpolate. Post. Officer sees the comment in the thread with the External chip and "📤 Sent to member". Member receives the SMS with a link.

9. **Member reads + replies.** Member clicks the SMS link. Public page renders the thread (only external comments visible). Member sees "Hi Eric, we need..." → clicks reply → types "Sending now" → submits. Officer reload — the member's reply appears with the member's name and the "📥 Reply" chip.

10. **Pin + edit + delete.** Pin one of the internal comments — it moves to the pinned section at top. Edit it; "(edited)" shows. Hover → previous version visible. Delete it; body becomes `[deleted]` but the row stays, audit log records.

11. **Search.** Type "employer" in the search box. Only matching comments remain visible. Clear the search; full thread restores.

## Idempotency / safety

- Document upload is per-row, no batch dependencies. The supersede logic is atomic in tx — old row's `is_current=false` and new row's insert commit together.
- Required-docs gate uses the same JSON-with-reason 409 pattern as the collateral gate so the inbox UI surfaces the missing list inline.
- Score history is append-only — never updated. Re-scores are insert-only.
- Comment edit preserves previous body in `edit_history`. Soft delete is reversible (restore = clear the deleted marker).
- External SMS firing is best-effort outside the comment-post tx (comment commits first; SMS may retry async via the existing notification queue). The audit trail records both the post and the SMS dispatch separately.
- Public comment-reply route is rate-limited per IP + per token to prevent enumeration.
- Bundle generation is cached for 60s per `(target_id, max_uploaded_at)` so repeat clicks don't regenerate.
- All new tables RLS-scoped on `tenant_id`.
- `gofmt`, `go vet`, `go test ./services/savings/... ./services/notification/...`, `pnpm test`, `pnpm build` all green.

When you're done, paste into the PR description: a screenshot of the Documents tab with the checklist showing mixed states (✓/✗/⚠), a screenshot of the Score tab in a passing state with the header + waterfall + multiplier + flags + per-factor + history all visible, a screenshot of the Comments tab with both internal and external comments and a member reply, plus the schema diff and the diff stat.