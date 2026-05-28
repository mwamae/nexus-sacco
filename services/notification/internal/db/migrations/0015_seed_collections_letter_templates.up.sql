-- Loans Phase 4 — seed the four collections letter PDF templates per
-- tenant. The savings collections handler + collections-engine
-- worker call notifier.GeneratePDF with document_type
-- 'loan_pre_collection_letter' / 'loan_demand_letter' /
-- 'loan_final_demand_letter' / 'loan_legal_notice_letter'.
--
-- The four letters share a common style + structure; only the headline,
-- body copy, and signature line differ. Placeholders the generator fills:
--
--   {{tenant_name}} {{tenant_address}} {{footer_extra}} — auto by generator
--   {{generated_date}}                                  — auto
--   {{member_name}}, {{loan_no}}, {{dpd_days}}
--   {{principal_balance}}, {{interest_balance}}, {{penalty_balance}}
--   {{total_outstanding}}
--
-- Idempotent via UNIQUE (tenant_id, document_type, version_no).

-- ─────────── pre_collection (DPD ~7) ───────────
INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'loan_pre_collection_letter', 1, 'Pre-collection letter',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Pre-collection letter</title>
<style>
  @page { size: A4; margin: 25mm 25mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 11pt; line-height: 1.5; }
  .head { border-bottom: 2px solid #2c5282; padding-bottom: 10px; margin-bottom: 18px; }
  .head .name { font-size: 18pt; color: #2c5282; font-weight: 700; }
  .head .addr { color: #555; font-size: 9pt; margin-top: 4px; }
  .meta { display: flex; justify-content: space-between; color: #555; font-size: 9pt; margin: 12px 0 24px; }
  h1 { color: #2c5282; font-size: 14pt; margin: 18px 0 14px; }
  table.summary { width: 100%; border-collapse: collapse; margin: 12px 0 18px; }
  table.summary td { padding: 6px 8px; border-bottom: 1px dotted #ccc; font-size: 10pt; }
  table.summary td.lbl { color: #555; width: 55%; }
  table.summary td.amt { font-family: 'Courier New', monospace; text-align: right; }
  .body p { margin: 10px 0; }
  .sig { margin-top: 36px; }
  .footer { margin-top: 32px; padding-top: 10px; border-top: 1px dashed #999; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div class="name">{{tenant_name}}</div>
    <div class="addr">{{tenant_address}}</div>
  </div>
  <div class="meta">
    <div>Date: {{generated_date}}</div>
    <div>Reference: {{loan_no}}</div>
  </div>
  <h1>Friendly payment reminder</h1>
  <div class="body">
    <p>Dear {{member_name}},</p>
    <p>This is a courtesy reminder that the scheduled instalment on your loan
       <strong>{{loan_no}}</strong> is now <strong>{{dpd_days}}</strong> day(s) past due.
       We're writing early so you can settle the account before any further charges accrue.</p>
    <table class="summary">
      <tr><td class="lbl">Principal balance</td><td class="amt">{{principal_balance}}</td></tr>
      <tr><td class="lbl">Interest balance</td><td class="amt">{{interest_balance}}</td></tr>
      <tr><td class="lbl">Penalty balance</td><td class="amt">{{penalty_balance}}</td></tr>
      <tr><td class="lbl"><strong>Total outstanding</strong></td><td class="amt"><strong>{{total_outstanding}}</strong></td></tr>
    </table>
    <p>Please remit any amount you can towards this balance, or contact your
       loan officer to agree a repayment plan. If the payment has already been
       made within the last 24 hours, kindly disregard this notice.</p>
    <div class="sig">Yours faithfully,<br/><br/><strong>Collections Desk</strong><br/>{{tenant_name}}</div>
  </div>
  <div class="footer">{{footer_extra}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── demand (DPD ~30) ───────────
INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'loan_demand_letter', 1, 'Demand letter',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Demand letter</title>
<style>
  @page { size: A4; margin: 25mm 25mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 11pt; line-height: 1.5; }
  .head { border-bottom: 2px solid #c97a00; padding-bottom: 10px; margin-bottom: 18px; }
  .head .name { font-size: 18pt; color: #c97a00; font-weight: 700; }
  .head .addr { color: #555; font-size: 9pt; margin-top: 4px; }
  .meta { display: flex; justify-content: space-between; color: #555; font-size: 9pt; margin: 12px 0 24px; }
  h1 { color: #c97a00; font-size: 14pt; margin: 18px 0 14px; }
  table.summary { width: 100%; border-collapse: collapse; margin: 12px 0 18px; }
  table.summary td { padding: 6px 8px; border-bottom: 1px dotted #ccc; font-size: 10pt; }
  table.summary td.lbl { color: #555; width: 55%; }
  table.summary td.amt { font-family: 'Courier New', monospace; text-align: right; }
  .body p { margin: 10px 0; }
  .sig { margin-top: 36px; }
  .stamp { display: inline-block; padding: 4px 10px; border: 2px solid #c97a00; color: #c97a00; font-weight: 700; letter-spacing: 1px; }
  .footer { margin-top: 32px; padding-top: 10px; border-top: 1px dashed #999; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div class="name">{{tenant_name}}</div>
    <div class="addr">{{tenant_address}}</div>
  </div>
  <div class="meta">
    <div>Date: {{generated_date}}</div>
    <div>Reference: {{loan_no}} · <span class="stamp">DEMAND</span></div>
  </div>
  <h1>Demand for payment</h1>
  <div class="body">
    <p>Dear {{member_name}},</p>
    <p>Despite prior notices, your loan account <strong>{{loan_no}}</strong> remains in arrears.
       As of today the balance has been outstanding for <strong>{{dpd_days}}</strong> day(s).
       This letter constitutes a formal demand for payment of the outstanding amount.</p>
    <table class="summary">
      <tr><td class="lbl">Principal balance</td><td class="amt">{{principal_balance}}</td></tr>
      <tr><td class="lbl">Interest balance</td><td class="amt">{{interest_balance}}</td></tr>
      <tr><td class="lbl">Penalty balance</td><td class="amt">{{penalty_balance}}</td></tr>
      <tr><td class="lbl"><strong>Total outstanding</strong></td><td class="amt"><strong>{{total_outstanding}}</strong></td></tr>
    </table>
    <p>You are required to settle the full outstanding amount within
       <strong>fourteen (14) days</strong> of the date of this letter. Failure to do
       so will result in further recovery action, including potential escalation
       to our legal team and possible attachment of guarantors and securities
       pledged for this facility.</p>
    <p>If you require a payment plan, contact our Collections Desk
       immediately. We would prefer to resolve this matter amicably.</p>
    <div class="sig">Yours faithfully,<br/><br/><strong>Collections Manager</strong><br/>{{tenant_name}}</div>
  </div>
  <div class="footer">{{footer_extra}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── final_demand (DPD ~60) ───────────
INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'loan_final_demand_letter', 1, 'Final demand letter',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Final demand</title>
<style>
  @page { size: A4; margin: 25mm 25mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 11pt; line-height: 1.5; }
  .head { border-bottom: 3px solid #c33; padding-bottom: 10px; margin-bottom: 18px; }
  .head .name { font-size: 18pt; color: #c33; font-weight: 700; }
  .head .addr { color: #555; font-size: 9pt; margin-top: 4px; }
  .meta { display: flex; justify-content: space-between; color: #555; font-size: 9pt; margin: 12px 0 24px; }
  h1 { color: #c33; font-size: 16pt; margin: 18px 0 14px; }
  table.summary { width: 100%; border-collapse: collapse; margin: 12px 0 18px; }
  table.summary td { padding: 6px 8px; border-bottom: 1px dotted #ccc; font-size: 10pt; }
  table.summary td.lbl { color: #555; width: 55%; }
  table.summary td.amt { font-family: 'Courier New', monospace; text-align: right; }
  .body p { margin: 10px 0; }
  .sig { margin-top: 36px; }
  .stamp { display: inline-block; padding: 4px 10px; border: 2px solid #c33; color: #c33; font-weight: 700; letter-spacing: 1px; }
  .warn-box { background: #ffeaea; border-left: 4px solid #c33; padding: 12px 16px; margin: 16px 0; font-weight: 600; }
  .footer { margin-top: 32px; padding-top: 10px; border-top: 1px dashed #999; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div class="name">{{tenant_name}}</div>
    <div class="addr">{{tenant_address}}</div>
  </div>
  <div class="meta">
    <div>Date: {{generated_date}}</div>
    <div>Reference: {{loan_no}} · <span class="stamp">FINAL DEMAND</span></div>
  </div>
  <h1>FINAL DEMAND FOR PAYMENT</h1>
  <div class="body">
    <p>Dear {{member_name}},</p>
    <p>Your loan account <strong>{{loan_no}}</strong> remains unsettled despite our
       earlier demand. As of today it has been in arrears for
       <strong>{{dpd_days}}</strong> day(s).</p>
    <table class="summary">
      <tr><td class="lbl">Principal balance</td><td class="amt">{{principal_balance}}</td></tr>
      <tr><td class="lbl">Interest balance</td><td class="amt">{{interest_balance}}</td></tr>
      <tr><td class="lbl">Penalty balance</td><td class="amt">{{penalty_balance}}</td></tr>
      <tr><td class="lbl"><strong>Total outstanding</strong></td><td class="amt"><strong>{{total_outstanding}}</strong></td></tr>
    </table>
    <div class="warn-box">
      This is our FINAL demand. You have <strong>seven (7) days</strong> from the
      date of this letter to pay the full amount in cleared funds. Failing
      that, your file will be referred to our legal team for recovery action,
      which may include enforcement against guarantors, attachment of pledged
      securities, and civil suit.
    </div>
    <p>If you intend to settle, please contact the Collections Manager today.</p>
    <div class="sig">Yours faithfully,<br/><br/><strong>Collections Manager</strong><br/>{{tenant_name}}</div>
  </div>
  <div class="footer">{{footer_extra}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── legal_notice (DPD 90+) ───────────
INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'loan_legal_notice_letter', 1, 'Legal notice letter',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Legal notice</title>
<style>
  @page { size: A4; margin: 25mm 25mm; }
  body { font-family: 'Times New Roman', serif; color: #111; font-size: 11pt; line-height: 1.6; }
  .head { border-bottom: 3px double #111; padding-bottom: 10px; margin-bottom: 18px; }
  .head .name { font-size: 18pt; font-weight: 700; }
  .head .addr { color: #555; font-size: 9pt; margin-top: 4px; }
  .stamp { display: inline-block; padding: 4px 10px; border: 2px solid #111; font-weight: 700; letter-spacing: 2px; text-transform: uppercase; }
  .meta { display: flex; justify-content: space-between; color: #555; font-size: 9pt; margin: 12px 0 24px; }
  h1 { font-size: 14pt; margin: 18px 0 14px; text-transform: uppercase; letter-spacing: 1px; text-align: center; }
  table.summary { width: 100%; border-collapse: collapse; margin: 12px 0 18px; }
  table.summary td { padding: 6px 8px; border-bottom: 1px solid #888; font-size: 10pt; }
  table.summary td.lbl { width: 55%; }
  table.summary td.amt { font-family: 'Courier New', monospace; text-align: right; }
  .body p { margin: 10px 0; text-align: justify; }
  .sig { margin-top: 36px; }
  .footer { margin-top: 32px; padding-top: 10px; border-top: 1px solid #111; text-align: center; font-size: 8pt; color: #444; }
</style></head><body>
  <div class="head">
    <div class="name">{{tenant_name}}</div>
    <div class="addr">{{tenant_address}}</div>
  </div>
  <div class="meta">
    <div>Date: {{generated_date}}</div>
    <div>Reference: {{loan_no}} · <span class="stamp">Legal Notice</span></div>
  </div>
  <h1>Statutory Notice of Default</h1>
  <div class="body">
    <p>TO: {{member_name}}</p>
    <p>TAKE NOTICE THAT you stand in default of your loan obligation to
       {{tenant_name}} (the &quot;Society&quot;), particulars whereof are set out below:</p>
    <table class="summary">
      <tr><td class="lbl">Loan number</td><td class="amt">{{loan_no}}</td></tr>
      <tr><td class="lbl">Days past due</td><td class="amt">{{dpd_days}}</td></tr>
      <tr><td class="lbl">Principal balance</td><td class="amt">{{principal_balance}}</td></tr>
      <tr><td class="lbl">Interest balance</td><td class="amt">{{interest_balance}}</td></tr>
      <tr><td class="lbl">Penalty balance</td><td class="amt">{{penalty_balance}}</td></tr>
      <tr><td class="lbl"><strong>Total outstanding</strong></td><td class="amt"><strong>{{total_outstanding}}</strong></td></tr>
    </table>
    <p>You are HEREBY REQUIRED to pay to the Society the full sum specified
       above within <strong>thirty (30) days</strong> of the date of this notice. Should
       you fail to do so, the Society shall, without further reference to you,
       institute legal proceedings for recovery of the said sum together with
       interest, costs of suit, and any other relief the Court may deem just to
       grant. The Society reserves the right to enforce against the guarantor(s)
       to your loan and to realise all securities pledged.</p>
    <p>This notice is issued pursuant to the loan agreement executed between
       yourself and the Society and the prevailing law of the Republic of Kenya.</p>
    <div class="sig">Issued at <strong>{{tenant_name}}</strong>,<br/>this {{generated_date}}.<br/><br/>_________________________<br/>For: Collections / Legal</div>
  </div>
  <div class="footer">{{footer_extra}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;
