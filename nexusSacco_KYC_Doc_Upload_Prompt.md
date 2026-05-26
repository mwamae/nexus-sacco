# Add KYC document upload from the Member profile → Documents & KYC tab

## Symptom

When staff open a counterparty profile (`/members/{id}` or `/orgs/{id}`) and switch to **Documents & KYC**, the tab is read-only. It lists files that exist but offers no way to add a new one. For individuals it's an even thinner surface — just a table with `Kind / Uploaded / Size`, no upload control, no verify control, no delete, no per-document audit. Compliance officers cannot collect, refresh, or sign off on KYC artefacts from where they actually work — they only get to do it during the application flow, which is too narrow.

## What is already in place (do not rebuild)

The HTTP + storage + DB plumbing already exists; only the profile-tab UI is missing.

- **Individual documents** — `services/member/internal/handler/member.go::UploadDocument` (≈line 585) and `DownloadDocument` (≈line 675). Routes are `POST/GET /v1/counterparties/{id}/documents/{kind}` (`services/member/internal/handler/routes.go:55–57`). Permission: `members:edit` / `members:view`. Storage trip-wires (size cap, MIME allow-list, `Storage.Save` then `member_documents` upsert in one tx) are in place.
- **Org documents** — `services/member/internal/handler/org.go::UploadDocument` (≈line 628), `DownloadDocument` and `VerifyDocument`. Routes `POST/GET /v1/orgs/{id}/documents/{kind}`, plus `POST /v1/orgs/{id}/documents/{kind}/verify`. Permissions: `members:create` / `members:view` / `members:edit`. The org row carries `issue_date`, `expiry_date`, `verification`, `verified_by`, `verified_at`, `verification_note`.
- **API client** — `web/admin/src/api/client.ts`: `uploadMemberDocument`, `fetchMemberDocument`, `memberDocumentURL`; `uploadOrgDocument`, `fetchOrgDocument`, `verifyOrgDocument`. Types `DocumentKind` (line ≈1448) and `OrgDocKind` exist.
- **Tab host** — `web/admin/src/pages/CounterpartyProfile.tsx` lines 596–653. The `DocumentsTab` component branches by `entity.kind` and renders a static table.
- **DB shapes** — `services/member/internal/db/migrations/0001_init.up.sql:88–100` (`member_documents` — kinds: `signature | passport_photo | id_front | id_back`; **UNIQUE (counterparty_id, kind)**), `0002_orgs.up.sql:121–140` (`org_documents` — 16 enum kinds; **UNIQUE (org_id, kind)** plus issue/expiry/verification columns).

The two relevant individual-side gaps in the backend, which this prompt also closes, are: (1) no `VerifyDocument` endpoint for the individual side, (2) no `DELETE` endpoint anywhere. We add both so the profile-tab UI has a complete set of actions.

---

## Claude Code prompt — paste this verbatim

> You are working in the nexusSacco monorepo. The Member / Organisation profile has a "Documents & KYC" tab that is read-only. Make it the canonical KYC document workstation: staff can upload, refresh, preview, verify, and remove KYC artefacts from this one tab, for both individual and institutional counterparties. The HTTP / storage / DB primitives already exist — don't rebuild them; add a thin verify+delete pair on the individual side, then build the UI on top.
>
> **Scope**
>
> 1. Backend (Go, `services/member`):
>    a. Add `POST /v1/counterparties/{id}/documents/{kind}/verify` — same shape as the org-side `VerifyDocument`. Records `verification`, `verified_by`, `verified_at`, `verification_note`. Requires permission `members:edit`. Writes an audit entry `member.document_verified`.
>    b. Add optional `issue_date` / `expiry_date` query params to the existing `POST /v1/counterparties/{id}/documents/{kind}` (mirrors org-side). Persist them.
>    c. Add `DELETE /v1/counterparties/{id}/documents/{kind}` and `DELETE /v1/orgs/{id}/documents/{kind}`. Both must: (i) require `members:edit`, (ii) delete the storage blob before deleting the DB row inside a single tx (with `_ = h.Storage.Delete(path)` cleanup on rollback paths), (iii) write audit `member.document_removed` / `org.document_removed` with the kind + reason. This is a tracked deletion, not a hide — the audit row is the immutable trail.
>    d. **Schema additions** (one migration, `services/member/internal/db/migrations/0013_member_document_kyc_fields.up.sql`):
>       ```sql
>       BEGIN;
>       ALTER TABLE member_documents
>         ADD COLUMN issue_date         date,
>         ADD COLUMN expiry_date        date,
>         ADD COLUMN verification       doc_verification NOT NULL DEFAULT 'pending',
>         ADD COLUMN verified_by        uuid,
>         ADD COLUMN verified_at        timestamptz,
>         ADD COLUMN verification_note  text;
>       CREATE INDEX member_documents_expiry_idx
>         ON member_documents (tenant_id, expiry_date) WHERE expiry_date IS NOT NULL;
>       COMMIT;
>       ```
>       Mirror in `.down.sql` (drop the columns + index). Update `DocumentStore.UpsertTx` to read/write the new columns; update the `RETURNING` clause and `domain.Document`.
>    e. **Expand `document_kind` enum** — individuals need more than `signature | passport_photo | id_front | id_back` to be a real KYC workstation. Add (one migration, `0014_member_document_kind_expand.up.sql`):
>       `kra_pin_certificate`, `proof_of_address`, `bank_statement`, `payslip`, `employment_letter`, `business_permit`, `signed_application_form`, `next_of_kin_id`, `other`.
>       Use `ALTER TYPE document_kind ADD VALUE IF NOT EXISTS '...'` for each (no down — enum values cannot be safely dropped; document that in the file header). Update `parseDocKind` in `services/member/internal/handler/member.go` and `isAllowedMIME` to cover the new kinds (PDF + common image types are sufficient for v1).
>    f. **Unique-key change** — the existing `UNIQUE (counterparty_id, kind)` becomes a problem for `other` (someone could need to upload multiple "other" documents). Replace with a partial unique so 'other' can repeat:
>       ```sql
>       ALTER TABLE member_documents DROP CONSTRAINT IF EXISTS member_documents_member_id_kind_key;
>       ALTER TABLE member_documents DROP CONSTRAINT IF EXISTS member_documents_counterparty_id_kind_key;
>       CREATE UNIQUE INDEX member_documents_cp_kind_singular_idx
>         ON member_documents (counterparty_id, kind) WHERE kind <> 'other';
>       ```
>       Update `UpsertTx` to branch: for non-`other` kinds keep the upsert-on-conflict logic; for `other`, always insert a fresh row. The `other` rows carry `verification_note` as the human label.
>    g. Routes & permissions — register the new routes alongside the existing ones in `services/member/internal/handler/routes.go`. Use the same permission groupings (`members:edit` for verify+upload+delete; `members:view` for download). For the org-side verify route that already exists, keep semantics identical to the new individual-side one — share the response shape between them.
>    h. **Audit events** — new event keys: `member.document_uploaded` (exists, expand to include `issue_date`/`expiry_date` in the metadata), `member.document_verified`, `member.document_removed`. Mirror on the org side.
>
> 2. Frontend API client (`web/admin/src/api/client.ts`):
>    a. Extend `DocumentKind` to the new enum set. Add a typed `IndividualDocVerification` type identical to `DocVerification`.
>    b. Add `verifyMemberDocument`, `deleteMemberDocument`, `deleteOrgDocument`.
>    c. Update `uploadMemberDocument` to take optional `issue_date`, `expiry_date` (forward as query params, matching org-side).
>    d. Extend `ApiDocument` with `issue_date`, `expiry_date`, `verification`, `verified_by`, `verified_at`, `verification_note`.
>    e. Add a small label map `DOC_KIND_LABELS: Record<DocumentKind | OrgDocKind, string>` so the UI doesn't render snake_case to humans.
>
> 3. UI (`web/admin/src/pages/CounterpartyProfile.tsx` `DocumentsTab`):
>    Replace the read-only table with a real workstation. The tab must support:
>    - **Header bar** with a primary "Add document" button (gated on permission `members:edit`), a search box (filters by kind label / verification status), and a "Show expired only" toggle.
>    - **Cards list, not table.** One card per document with: kind label, status pill (`pending` / `verified` / `rejected`), filename + size + uploaded_at + uploaded_by, issue_date + expiry_date (red highlight if expired or expiring < 30 days), verification note (if any), and an actions row with Preview, Replace, Verify, Reject, Delete. The status pill is colour-coded (warn / pos / neg) matching `Badge` tones already used elsewhere in the file.
>    - **Add-document modal.** Reuse `ModalShell` (already in `Shares.tsx` — extract it to `web/admin/src/components/Modal.tsx` if it isn't already shared). Fields: kind picker (uses `DOC_KIND_LABELS`, hides kinds already on file for non-`other` rows), file input (accept = pdf/image/word), issue_date, expiry_date, verification_note (optional, used as label for `other`). Client-side validation: file ≤ 10 MB, MIME in allow-list, expiry_date ≥ issue_date.
>    - **Replace flow.** Tapping Replace opens the same modal pre-selected on the existing kind; submitting calls `uploadMemberDocument` with the same kind — the backend upserts. Show "Replacing version uploaded {date} by {who}" beneath the file input.
>    - **Verify / Reject flow.** Tapping Verify confirms inline (no modal — verify is a low-friction action), Reject opens a small dialog that requires a reason string. Both call `verifyMemberDocument` / `verifyOrgDocument`. Optimistic UI; revert on error.
>    - **Delete flow.** Confirm dialog `"Permanently delete the {label} for {member}? The original file will be removed from storage. This action is recorded in the audit log."` (deletion is permanent and tracked, not soft-deletable, per the SACCO compliance posture — match the wording used elsewhere for irreversible actions).
>    - **Preview.** For images, render via `memberDocumentURL` or an authed blob fetch. For PDFs, open in a new tab using a temporary object URL from `fetchMemberDocument`. Don't attempt previews for unknown MIMEs.
>    - **Org-side parity.** When `entity.kind === 'institutional'`, use the org endpoints (`uploadOrgDocument`, `verifyOrgDocument`, `deleteOrgDocument`) and the `OrgDocKind` enum. The card layout is identical; only the kind picker contents differ.
>    - **KYC summary banner** at the top of the tab: progress against a required-document checklist driven by the counterparty's `kind`. For individuals: `id_front`, `id_back`, `passport_photo`, `kra_pin_certificate`, `signature`. For institutions: `registration_certificate`, `kra_pin_certificate`, `board_resolution`, plus whatever the existing onboarding checklist already mandates (read `services/member/internal/store/org_store.go` or the onboarding spec — don't invent). The banner shows "{verified}/{required} verified · {pending} pending · {missing} missing" with a small progress bar.
>
> 4. **Audit surfacing.** The Activity tab already pulls audit entries; make sure the new event keys render with friendly labels (`member.document_verified` → "Document verified", etc.) — touch the audit-event label map (search `AuditTimeline` and any `eventLabel`/`AUDIT_LABELS` table).
>
> 5. **Tests.**
>    Go:
>    - `services/member/internal/handler/member_document_verify_test.go` — verify happy path, permission deny, audit row written.
>    - `services/member/internal/handler/member_document_delete_test.go` — happy path, storage cleanup confirmed, audit row, idempotent 404 on second delete.
>    - `services/member/internal/store/document_store_test.go` — upsert with new fields; `other`-kind multiple rows allowed; partial unique enforces singleton for fixed kinds.
>    - `services/member/internal/db/migrations/` — confirm `.down.sql` round-trip for `0013` and document that `0014` (enum expansion) is forward-only in the file comment.
>    React:
>    - `web/admin/src/pages/CounterpartyProfile.documents.test.tsx` — render the tab with permission on/off, verify the Add button shows/hides, modal validation, optimistic verify, expired-only filter behaviour.
>
> 6. **RLS / multi-tenancy.** Re-read the existing tx callers — every new endpoint must go through `h.DB.WithTenantTx(...)`. Add a regression test that hits the verify + delete endpoints with a wrong-tenant counterparty id and asserts 404.
>
> **Acceptance walkthrough**
>
> 1. Sign in, open an individual member profile, Documents & KYC.
> 2. KYC progress banner shows "0/5 verified · 0 pending · 5 missing".
> 3. Click "Add document". Pick "KRA PIN certificate", attach a PDF, set issue_date = today, expiry = +24 months, submit. Card appears with `pending` pill.
> 4. Click Verify. Pill turns `verified`. Banner shows "1/5 verified".
> 5. Click Replace on the same card, upload a new file. Old blob is gone from storage; new card shows updated `uploaded_at` and `pending` pill again.
> 6. Click Delete. Modal warns about permanence; on confirm the card disappears, Activity tab shows `Document removed · KRA PIN certificate · {staff}` with the reason.
> 7. Switch to an institutional counterparty. Same flow works against the org endpoints; the kind picker shows the 16 org doc kinds. Upload `registration_certificate`, then `board_resolution`, then verify both. Banner reflects org's required set.
> 8. As a non-`members:edit` user, every action is hidden; the tab is still readable (download + preview only).
> 9. SQL: `SELECT counterparty_id, kind, verification, expiry_date FROM member_documents` reflects every state change; no orphan files on disk after the delete (eyeball `Storage.Open` returning ErrNotFound for the old path).
>
> **Idempotency / safety**
>
> - Storage delete in the delete handler must run **after** the DB row is removed but **inside** the same outer handler scope; if storage delete fails we log it and return 200 anyway (the row is gone; storage drift is acceptable and surfaced via a future janitor). Don't roll back the DB on storage failure — that would resurrect a row whose file the user already saw vanish.
> - Treat the upload + DB upsert as already-correct (the on-conflict logic is fine); only the new columns flow through.
> - No table renames, no breaking-change semantics. Keep this PR self-contained and reviewable.
> - `gofmt`, `go vet`, `go test ./services/member/...`, `pnpm test` (or `npm test`) all pass.
> - Update `docs/` if a KYC ops guide exists; otherwise skip.
>
> When done, paste the diff stat + new audit-event keys + new route table into the PR description.

---

## Why this shape

The Documents & KYC tab is the natural workstation for compliance — making it the canonical place to add, verify, refresh, and remove KYC artefacts means staff don't have to bounce through the onboarding flow for re-KYC, expiring documents, or supplementary uploads. The backend already handles uploads safely; the missing surface area (verify + delete for individuals, expiry/verification columns) is small. The enum expansion + partial unique on `other` future-proofs the schema without forcing every new doc type to land as its own enum value.