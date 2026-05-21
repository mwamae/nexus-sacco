-- ═══════════════════════════════════════════════════════════════════
-- Stage 5 — PDF generation, storage, and attachment.
--
-- Adds:
--   • pdf_templates             — per-tenant, per-document-type HTML
--     template body + branding metadata. Versioned: every save inserts
--     a new row with version_no = max + 1; the most recent active
--     version is what renders go through.
--   • pdf_documents             — one row per generated PDF. Captures
--     storage path, byte size, who/when, source record, and a
--     time-limited download token for ephemeral re-fetch from email
--     links sent to members.
--   • notification_deliveries.attachment_paths — array of paths the
--     email worker tacks on as MIME attachments at send time.
-- ═══════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS pdf_templates (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  document_type   text NOT NULL,                -- 'OFFER_LETTER', 'SHARE_CERTIFICATE', ...
  version_no      int  NOT NULL DEFAULT 1,
  -- Display label shown in the admin template manager (Stage 8).
  label           text NOT NULL,
  -- Full HTML — chromedp renders this to A4 PDF. {{var}} placeholders.
  html_body       text NOT NULL,
  -- Page size hint for the renderer. A4 is the most common.
  page_size       text NOT NULL DEFAULT 'A4'
                    CHECK (page_size IN ('A4', 'Letter', 'Legal')),
  is_active       boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid,
  UNIQUE (tenant_id, document_type, version_no)
);
CREATE INDEX IF NOT EXISTS pdf_templates_lookup_idx
  ON pdf_templates (tenant_id, document_type, version_no DESC)
  WHERE is_active = true;

CREATE TABLE IF NOT EXISTS pdf_documents (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id           uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  document_type       text NOT NULL,
  template_id         uuid REFERENCES pdf_templates(id) ON DELETE SET NULL,
  template_version    int,

  -- Subject record. Exactly one is set in practice (loan, member,
  -- share account, etc.) — but the table doesn't enforce that since
  -- the platform may grow doc types whose subject is something else.
  subject_member_id   uuid,
  subject_loan_id     uuid,
  subject_account_id  uuid,
  subject_label       text NOT NULL DEFAULT '',  -- human description e.g. "Offer · LA-2026-00001"

  -- Snapshot of the variables used at render time. Useful for audit
  -- and for regenerating the same PDF later if the template changes.
  payload             jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- Filesystem-relative path (under NOTIFICATION_PDF_DIR). Never
  -- exposed in a URL; the download endpoint streams from disk.
  storage_path        text NOT NULL,
  file_size_bytes     int  NOT NULL DEFAULT 0,

  -- Ephemeral signed download URL (Stage 5). The token is a random
  -- value; the URL is /d/<token>.pdf and is valid until expires_at.
  download_token      text UNIQUE,
  token_expires_at    timestamptz,

  -- Audit
  download_count      int  NOT NULL DEFAULT 0,
  last_downloaded_at  timestamptz,
  generated_at        timestamptz NOT NULL DEFAULT now(),
  generated_by        uuid
);
CREATE INDEX IF NOT EXISTS pdf_documents_subject_member_idx
  ON pdf_documents (tenant_id, subject_member_id, generated_at DESC)
  WHERE subject_member_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS pdf_documents_subject_loan_idx
  ON pdf_documents (tenant_id, subject_loan_id, generated_at DESC)
  WHERE subject_loan_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS pdf_documents_type_idx
  ON pdf_documents (tenant_id, document_type, generated_at DESC);

-- Email worker reads this when sending; populated by the notify handler
-- when the caller asks for a PDF to be attached.
ALTER TABLE notification_deliveries
  ADD COLUMN IF NOT EXISTS attachment_paths text[] NOT NULL DEFAULT ARRAY[]::text[];

-- RLS
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT unnest(ARRAY['pdf_templates', 'pdf_documents'])
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($q$
      CREATE POLICY tenant_isolation_%I ON %I
        USING (tenant_id = current_tenant_id())
        WITH CHECK (tenant_id = current_tenant_id())
    $q$, t, t);
  END LOOP;
END $$;

GRANT SELECT, INSERT, UPDATE, DELETE ON pdf_templates, pdf_documents TO nexus_app;

-- ─────────── Seed default templates ───────────
--
-- Two doc types ship in Stage 5 as proof of the framework:
--   • OFFER_LETTER       — loan offer letter
--   • SHARE_CERTIFICATE  — numbered share certificate
--
-- The remaining 7 (Statement, Loan Agreement, Settlement Cert,
-- Restructure, Interest Advice, Dividend Advice, Approval Pack)
-- follow the exact same pattern and land in stage 5b.
--
-- These templates use a small set of {{tenant_*}} placeholders that
-- the renderer injects from the tenants/tenant_branding tables.

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body)
SELECT t.id, b.code, 1, b.label, b.html
FROM tenants t
CROSS JOIN (VALUES
  (
    'OFFER_LETTER',
    'Loan offer letter',
    $html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Loan Offer</title>
<style>
  @page { size: A4; margin: 30mm 20mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 11pt; line-height: 1.5; }
  .head { display: flex; align-items: center; justify-content: space-between; border-bottom: 2px solid #2c5282; padding-bottom: 10px; margin-bottom: 30px; }
  .head .name { font-size: 20pt; color: #2c5282; font-weight: 600; }
  .head .meta { text-align: right; color: #555; font-size: 9pt; }
  h1 { font-size: 16pt; color: #2c5282; margin-top: 30px; }
  h2 { font-size: 12pt; margin-top: 24px; color: #444; }
  .terms { background: #f5f7fb; border: 1px solid #d8dee8; padding: 15px 20px; margin: 20px 0; }
  .terms table { width: 100%; border-collapse: collapse; }
  .terms td { padding: 6px 0; }
  .terms td:first-child { color: #666; width: 50%; }
  .terms td:last-child { text-align: right; font-weight: 600; }
  .fees { width: 100%; border-collapse: collapse; margin-top: 12px; }
  .fees th, .fees td { padding: 6px 8px; text-align: left; border-bottom: 1px solid #eee; }
  .fees th { background: #f9f9fc; }
  .fees td:last-child, .fees th:last-child { text-align: right; }
  .total { font-weight: 700; }
  .footer { margin-top: 60px; font-size: 9pt; color: #888; border-top: 1px solid #ddd; padding-top: 10px; }
  .sig-blocks { display: flex; gap: 40px; margin-top: 40px; }
  .sig-blocks > div { flex: 1; border-top: 1px solid #444; padding-top: 6px; font-size: 9pt; color: #666; }
</style></head>
<body>
  <div class="head">
    <div>
      <div class="name">{{tenant_name}}</div>
      <div style="font-size: 9pt; color: #555;">{{tenant_address}}</div>
    </div>
    <div class="meta">
      <div><strong>OFFER LETTER</strong></div>
      <div>Date: {{generated_date}}</div>
      <div>Ref: {{application_no}}</div>
    </div>
  </div>

  <p>Dear <strong>{{member_name}}</strong> (Member: {{member_no}}),</p>
  <p>We are pleased to offer you a loan facility on the following terms and conditions.</p>

  <div class="terms">
    <table>
      <tr><td>Loan amount</td><td>KES {{approved_amount}}</td></tr>
      <tr><td>Tenure</td><td>{{term_months}} months</td></tr>
      <tr><td>Interest rate</td><td>{{interest_rate}}% per annum</td></tr>
      <tr><td>Interest method</td><td>{{interest_method}}</td></tr>
      <tr><td>Repayment</td><td>{{repayment_method}}</td></tr>
      <tr><td>Monthly installment</td><td>KES {{monthly_installment}}</td></tr>
      <tr><td>Total interest payable</td><td>KES {{total_interest}}</td></tr>
      <tr><td>Total payable</td><td>KES {{total_payable}}</td></tr>
      <tr><td><strong>Net disbursed (after fees)</strong></td><td><strong>KES {{net_disbursed}}</strong></td></tr>
    </table>
  </div>

  <h2>Fees and charges</h2>
  {{fees_html}}

  <h2>Terms and conditions</h2>
  <ol>
    <li>This offer is valid for fourteen (14) days from the date above.</li>
    <li>Repayment is due monthly on the same calendar day as the disbursement.</li>
    <li>Failure to repay any installment within seven (7) days of the due date attracts a penalty at the rate published in the SACCO bylaws.</li>
    <li>The SACCO reserves the right to recover any outstanding balance from your savings, shares, or guarantors' holdings on default.</li>
    <li>All disputes shall be resolved per the Co-operative Societies Act of Kenya.</li>
  </ol>

  <p style="margin-top: 30px;">By accepting this offer through the member portal, you confirm that you have read, understood, and agreed to the terms above.</p>

  <div class="sig-blocks">
    <div>Borrower's signature &amp; date</div>
    <div>For {{tenant_name}}</div>
  </div>

  <div class="footer">
    {{tenant_name}} · {{tenant_address}}{{footer_extra}}
  </div>
</body></html>$html$
  ),
  (
    'SHARE_CERTIFICATE',
    'Share certificate',
    $html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Share Certificate</title>
<style>
  @page { size: A4 landscape; margin: 25mm 30mm; }
  body { font-family: Georgia, 'Times New Roman', serif; color: #222; }
  .frame { border: 4px double #2c5282; padding: 30mm 25mm; height: 100%; }
  .head { text-align: center; }
  .head h1 { color: #2c5282; font-size: 28pt; margin: 0; letter-spacing: 2px; }
  .head .sub { color: #777; font-size: 11pt; margin-top: 6px; letter-spacing: 6px; text-transform: uppercase; }
  .body { text-align: center; margin-top: 40px; font-size: 14pt; line-height: 1.8; }
  .body .member { font-size: 22pt; color: #2c5282; font-weight: 700; letter-spacing: 1px; margin: 12px 0; }
  .body .qty { font-size: 36pt; color: #2c5282; font-weight: 700; margin: 18px 0; }
  .body .value { color: #444; }
  .cert-no { position: absolute; top: 40mm; right: 40mm; font-size: 10pt; color: #777; }
  .footer { margin-top: 50px; display: flex; justify-content: space-between; }
  .footer > div { width: 30%; border-top: 1px solid #444; padding-top: 6px; text-align: center; font-size: 10pt; color: #666; }
</style></head>
<body>
  <div class="frame">
    <div class="cert-no">Certificate No: <strong>{{certificate_no}}</strong></div>
    <div class="head">
      <h1>{{tenant_name}}</h1>
      <div class="sub">Share Certificate</div>
    </div>
    <div class="body">
      <div>This certifies that</div>
      <div class="member">{{member_name}}</div>
      <div>Member Number {{member_no}}</div>
      <div>is the registered holder of</div>
      <div class="qty">{{shares_held}} shares</div>
      <div class="value">at a par value of KES {{par_value}} each.</div>
      <div style="margin-top: 24px; font-size: 11pt; color: #777;">Issued on {{generated_date}}</div>
    </div>
    <div class="footer">
      <div>Secretary</div>
      <div>Chairperson</div>
      <div>{{tenant_name}}</div>
    </div>
  </div>
</body></html>$html$
  )
) AS b(code, label, html)
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;
