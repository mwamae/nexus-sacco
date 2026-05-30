-- Wire the three remaining placeholder tabs (Documents, Score, Comments)
-- on the loan application + loan detail pages.
--
--   1. Documents lifecycle  — review state, expiry, versioning, per-product
--      required-kinds checklist, per-tenant expiry windows.
--   2. Score history        — append-only audit of every re-scoring event.
--   3. Comments              — hybrid internal + external thread with pinning,
--      edit history, reply token for member SMS replies, plus a starter set
--      of templates seeded per tenant.
--
-- See nexusSacco_Loan_Tabs_Documents_Score_Comments_Prompt.md.

BEGIN;

-- ─────────── 1. Documents: required-kinds + expiry + review + versioning ───────────

ALTER TABLE loan_products
  ADD COLUMN IF NOT EXISTS required_document_kinds text[] NOT NULL DEFAULT ARRAY[]::text[];

COMMENT ON COLUMN loan_products.required_document_kinds IS
  'Array of loan_doc_kind enum values (as text). The workflow approve gate refuses to advance an application until each named kind has at least one current non-expired document attached.';

-- Per-tenant defaults the new-product form inherits, plus the per-kind
-- expiry window (days from upload) + the warn-window for the UI banner.
ALTER TABLE tenant_operations
  ADD COLUMN IF NOT EXISTS default_required_document_kinds text[] NOT NULL DEFAULT ARRAY['id_copy','payslip','bank_statement']::text[],
  ADD COLUMN IF NOT EXISTS document_expiry_windows jsonb NOT NULL DEFAULT '{
    "id_copy":             1825,
    "payslip":             60,
    "bank_statement":      90,
    "mpesa_statement":     90,
    "business_financials": 365,
    "valuation_report":    730
  }'::jsonb,
  ADD COLUMN IF NOT EXISTS document_expiry_warning_days int NOT NULL DEFAULT 14
    CHECK (document_expiry_warning_days BETWEEN 1 AND 365);

-- loan_documents lifecycle.
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

-- ─────────── 2. Score history (append-only) ───────────

CREATE TABLE loan_application_score_history (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id           uuid NOT NULL REFERENCES loan_applications(id) ON DELETE CASCADE,
  scored_at                timestamptz NOT NULL DEFAULT now(),
  scored_by                uuid,
  credit_score             int,
  risk_band                text,
  affordability_pass       boolean,
  dti_ratio                numeric(6,3),
  net_disposable_income    numeric(18,2),
  computed_max_amount      numeric(18,2),
  computed_max_installment numeric(18,2),
  recommended_amount       numeric(18,2),
  recommended_term_months  int,
  scoring_details          jsonb,
  scoring_flags            jsonb,
  trigger_reason           text                          -- 'initial_score' | 'manual_rescore' | 'guarantor_changed' | 'document_added'
);

CREATE INDEX loan_app_score_history_app_idx
  ON loan_application_score_history (application_id, scored_at DESC);

ALTER TABLE loan_application_score_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_application_score_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_app_score_history ON loan_application_score_history
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT ON loan_application_score_history TO nexus_app;

-- ─────────── 3. Comments (hybrid internal + external) ───────────

CREATE TABLE loan_comments (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  application_id      uuid REFERENCES loan_applications(id) ON DELETE CASCADE,
  loan_id             uuid REFERENCES loans(id) ON DELETE CASCADE,
  parent_id           uuid REFERENCES loan_comments(id) ON DELETE CASCADE,
  visibility          text NOT NULL CHECK (visibility IN ('internal','external')),
  body                text NOT NULL CHECK (length(body) > 0),
  attachment_paths    jsonb NOT NULL DEFAULT '[]'::jsonb,
  author_user_id      uuid,                                     -- NULL when from member reply
  author_member_id    uuid,                                     -- NULL when from officer
  posted_at           timestamptz NOT NULL DEFAULT now(),
  edited_at           timestamptz,
  edit_history        jsonb NOT NULL DEFAULT '[]'::jsonb,
  pinned              boolean NOT NULL DEFAULT false,
  member_read_at      timestamptz,
  reply_token         uuid UNIQUE,                              -- outbound SMS carries this so a member-link reply attaches to the right thread
  is_deleted          boolean NOT NULL DEFAULT false,
  CHECK ((application_id IS NOT NULL) <> (loan_id IS NOT NULL)),
  CHECK ((author_user_id IS NOT NULL) <> (author_member_id IS NOT NULL))
);

CREATE INDEX loan_comments_app_idx
  ON loan_comments (application_id, posted_at DESC)
  WHERE application_id IS NOT NULL;
CREATE INDEX loan_comments_loan_idx
  ON loan_comments (loan_id, posted_at DESC)
  WHERE loan_id IS NOT NULL;
CREATE INDEX loan_comments_pinned_idx
  ON loan_comments (tenant_id, pinned)
  WHERE pinned = true;
CREATE INDEX loan_comments_external_unread_idx
  ON loan_comments (tenant_id, posted_at DESC)
  WHERE visibility = 'external' AND member_read_at IS NULL;

ALTER TABLE loan_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE loan_comments FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_comments ON loan_comments
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE ON loan_comments TO nexus_app;

-- Templates — small starter set per tenant + per-tenant additions.
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
ALTER TABLE loan_comment_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_loan_comment_templates ON loan_comment_templates
  USING (tenant_id = current_tenant_id())
  WITH CHECK (tenant_id = current_tenant_id());

GRANT SELECT, INSERT, UPDATE, DELETE ON loan_comment_templates TO nexus_app;

-- Seed external templates per existing tenant.
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

-- And a small set of internal templates.
INSERT INTO loan_comment_templates (tenant_id, label, visibility, body)
SELECT t.id, x.label, 'internal', x.body
  FROM tenants t
  CROSS JOIN (VALUES
    ('Employment verified', 'Spoke with employer ({contact}) — employment confirmed.'),
    ('Site visit complete', 'Conducted site visit on {date}. Findings: {findings}.'),
    ('Recommend approval',  'Recommending approval at {amount} / {term} months. Risk acceptable given {rationale}.')
  ) AS x(label, body)
ON CONFLICT (tenant_id, label) DO NOTHING;

-- SECURITY DEFINER bridge so the public /p/comments/{token}/* route can
-- discover the tenant from a reply_token before opening the tenant-scoped
-- tx. Mirrors find_guarantor_token_tenant().
CREATE OR REPLACE FUNCTION find_comment_token_tenant(p_token uuid)
RETURNS TABLE (comment_id uuid, tenant_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  SELECT id, tenant_id FROM loan_comments WHERE reply_token = p_token LIMIT 1
$$;
REVOKE ALL ON FUNCTION find_comment_token_tenant(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION find_comment_token_tenant(uuid) TO nexus_app;

COMMIT;
