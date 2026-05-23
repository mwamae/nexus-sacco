-- Collection Desk needs a printable receipt. We seed a CASH_RECEIPT
-- pdf_templates row per tenant — same shape as OFFER_LETTER /
-- SHARE_CERTIFICATE (migration 0005). Idempotent via the existing
-- UNIQUE (tenant_id, document_type, version_no) constraint.
--
-- Template placeholders the savings handler fills in:
--   {{tenant_name}}, {{tenant_address}}, {{footer_extra}}  — auto by generator
--   {{generated_date}}                                     — auto
--   {{serial}}            — receipt serial (R-T1-YYYYMMDD-NNNN)
--   {{till_code}}         — physical till code OR channel slug (mpesa, etc.)
--   {{cashier_name}}      — best-effort; falls back to a uuid prefix
--   {{cp_display_name}}, {{cp_cp_number}}, {{cp_legacy_id}}
--   {{value_date}}, {{narration}}
--   {{channel}}, {{channel_ref}}, {{channel_amount}}
--   {{lines_html}}        — pre-rendered <tr>...</tr> rows (the
--                            simple {{var}} engine doesn't support
--                            loops; savings builds the rows string)
--
-- The HTML is intentionally print-oriented — A5 portrait, narrow
-- margins, large monospaced amounts. Matches a typical till-printer
-- receipt shape but renders cleanly via chromedp at A4 too.

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'CASH_RECEIPT', 1, 'Cash receipt',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Receipt {{serial}}</title>
<style>
  /* Receipt-style layout on A4 — narrow column centered, like a
     till slip printed on standard paper. Constraint allows A4/Letter/Legal. */
  @page { size: A4; margin: 20mm 35mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 10pt; line-height: 1.4; }
  .head { text-align: center; border-bottom: 2px solid #2c5282; padding-bottom: 8px; margin-bottom: 12px; }
  .head .name { font-size: 16pt; color: #2c5282; font-weight: 700; }
  .head .meta { color: #555; font-size: 8pt; margin-top: 4px; }
  .serial { text-align: center; font-family: 'Courier New', monospace; font-size: 11pt; color: #2c5282; margin: 4px 0 14px; letter-spacing: 1px; }
  .row { display: flex; justify-content: space-between; margin: 3px 0; font-size: 9pt; }
  .row .lbl { color: #777; text-transform: uppercase; font-size: 8pt; }
  table.lines { width: 100%; border-collapse: collapse; margin-top: 12px; }
  table.lines th, table.lines td { padding: 5px 4px; text-align: left; font-size: 9pt; }
  table.lines thead th { border-bottom: 1px solid #2c5282; color: #2c5282; font-size: 8pt; text-transform: uppercase; letter-spacing: 0.5px; }
  table.lines tbody tr { border-bottom: 1px dotted #ccc; }
  table.lines td.amt { text-align: right; font-family: 'Courier New', monospace; }
  .total { display: flex; justify-content: space-between; margin-top: 10px; padding-top: 8px; border-top: 2px solid #222; font-size: 12pt; font-weight: 700; }
  .total .amt { font-family: 'Courier New', monospace; }
  .channel-block { margin-top: 14px; padding: 8px 10px; background: #f5f7fb; border-left: 3px solid #2c5282; }
  .channel-block .row { margin: 2px 0; }
  .narration { margin-top: 10px; font-size: 9pt; color: #555; font-style: italic; }
  .footer { margin-top: 18px; padding-top: 8px; border-top: 1px dashed #999; text-align: center; font-size: 8pt; color: #666; }
  .footer .small { font-size: 7pt; color: #999; margin-top: 4px; }
</style></head><body>
  <div class="head">
    <div class="name">{{tenant_name}}</div>
    <div class="meta">{{tenant_address}}</div>
    <div class="meta">Till {{till_code}} · {{generated_date}}</div>
  </div>
  <div class="serial">{{serial}}</div>

  <div class="row"><span class="lbl">Counterparty</span><span><strong>{{cp_display_name}}</strong></span></div>
  <div class="row"><span class="lbl">CP number</span><span style="font-family:'Courier New', monospace">{{cp_cp_number}}</span></div>
  <div class="row"><span class="lbl">Legacy ID</span><span style="font-family:'Courier New', monospace">{{cp_legacy_id}}</span></div>
  <div class="row"><span class="lbl">Value date</span><span>{{value_date}}</span></div>

  <table class="lines">
    <thead><tr><th>#</th><th>Description</th><th class="amt">Amount (KES)</th></tr></thead>
    <tbody>
      {{lines_html}}
    </tbody>
  </table>

  <div class="total">
    <span>Total</span>
    <span class="amt">KES {{channel_amount}}</span>
  </div>

  <div class="channel-block">
    <div class="row"><span class="lbl">Channel</span><span>{{channel}}</span></div>
    <div class="row"><span class="lbl">Reference</span><span style="font-family:'Courier New', monospace">{{channel_ref}}</span></div>
    <div class="row"><span class="lbl">Cashier</span><span>{{cashier_name}}</span></div>
  </div>

  {{narration_block}}

  <div class="footer">
    Thank you for your contribution.
    <div class="small">{{footer_extra}}</div>
  </div>
</body></html>$html$,
'A4'
FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;
