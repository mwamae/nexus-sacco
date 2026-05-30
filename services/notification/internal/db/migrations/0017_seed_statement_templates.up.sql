-- DSID Phase 2.1 — seed the four statement PDF templates per tenant.
--
-- The template engine in this service is plain {{var}} substitution
-- (services/notification/internal/store/template_engine.go) — no loops.
-- The savings handler pre-renders row HTML server-side and passes it
-- as a single placeholder (e.g. {{transactions_table}}). Keeps the
-- engine simple + makes the templates fully editable per-tenant.

-- ─────────── 1. deposit_statement ───────────

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'deposit_statement', 1, 'Deposit account statement',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Deposit statement</title>
<style>
  @page { size: A4; margin: 20mm 20mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 10pt; line-height: 1.45; }
  .head { border-bottom: 2px solid #2c5282; padding-bottom: 10px; margin-bottom: 16px; display: flex; justify-content: space-between; align-items: flex-end; }
  .head .name { font-size: 18pt; color: #2c5282; font-weight: 700; }
  .head .addr { color: #555; font-size: 8.5pt; margin-top: 3px; }
  .head .right { text-align: right; }
  .meta { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; padding: 10px; background: #f7f9fc; border-radius: 6px; margin-bottom: 16px; font-size: 9.5pt; }
  .meta .lbl { color: #666; font-size: 8.5pt; text-transform: uppercase; letter-spacing: 0.5px; }
  .meta .val { font-weight: 600; margin-top: 2px; }
  h1 { color: #2c5282; font-size: 13pt; margin: 16px 0 10px; }
  table.txns { width: 100%; border-collapse: collapse; }
  table.txns th { background: #2c5282; color: white; padding: 6px 8px; text-align: left; font-size: 9pt; font-weight: 600; }
  table.txns th.num { text-align: right; }
  table.txns td { padding: 5px 8px; border-bottom: 1px solid #eee; font-size: 9.5pt; }
  table.txns td.num { text-align: right; font-family: 'Courier New', monospace; }
  table.txns tr.opening, table.txns tr.closing { background: #f7f9fc; font-weight: 700; }
  .footer { margin-top: 26px; padding-top: 10px; border-top: 1px dashed #aaa; text-align: center; font-size: 8pt; color: #666; }
  .signature { margin-top: 30px; display: flex; justify-content: space-between; }
  .signature .block { width: 45%; text-align: center; padding-top: 30px; border-top: 1px solid #555; font-size: 9pt; color: #555; }
</style></head><body>
  <div class="head">
    <div>
      <div class="name">{{tenant_name}}</div>
      <div class="addr">{{tenant_address}}</div>
    </div>
    <div class="right">
      <div style="font-weight:700;font-size:12pt;">DEPOSIT STATEMENT</div>
      <div class="addr">Generated {{generated_date}}</div>
    </div>
  </div>
  <div class="meta">
    <div><div class="lbl">Member</div><div class="val">{{member_name}}</div><div class="addr">{{member_no}}</div></div>
    <div><div class="lbl">Period</div><div class="val">{{period_label}}</div></div>
    <div><div class="lbl">Account</div><div class="val">{{account_label}}</div></div>
    <div><div class="lbl">Currency</div><div class="val">{{currency}}</div></div>
  </div>
  <h1>Transactions</h1>
  <table class="txns">
    <thead><tr>
      <th>Date</th><th>Ref</th><th>Type</th><th>Channel</th><th>Narration</th>
      <th class="num">Amount</th><th class="num">Balance</th>
    </tr></thead>
    <tbody>{{transactions_html}}</tbody>
  </table>
  <div class="signature">
    <div class="block">Authorised officer</div>
    <div class="block">Member acknowledgement</div>
  </div>
  <div class="footer">{{tenant_disclaimer}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── 2. share_statement ───────────

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'share_statement', 1, 'Share account statement',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Share statement</title>
<style>
  @page { size: A4; margin: 20mm 20mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 10pt; line-height: 1.45; }
  .head { border-bottom: 2px solid #146c43; padding-bottom: 10px; margin-bottom: 16px; display: flex; justify-content: space-between; align-items: flex-end; }
  .head .name { font-size: 18pt; color: #146c43; font-weight: 700; }
  .head .addr { color: #555; font-size: 8.5pt; margin-top: 3px; }
  .head .right { text-align: right; }
  .meta { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; padding: 10px; background: #f3faf5; border-radius: 6px; margin-bottom: 16px; font-size: 9.5pt; }
  .meta .lbl { color: #666; font-size: 8.5pt; text-transform: uppercase; letter-spacing: 0.5px; }
  .meta .val { font-weight: 600; margin-top: 2px; }
  h1 { color: #146c43; font-size: 13pt; margin: 16px 0 10px; }
  .summary { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-bottom: 16px; }
  .summary .card { padding: 10px; background: #f3faf5; border-radius: 6px; }
  .summary .card .lbl { color: #666; font-size: 8.5pt; text-transform: uppercase; }
  .summary .card .val { font-weight: 700; font-size: 13pt; margin-top: 4px; color: #146c43; }
  table.txns { width: 100%; border-collapse: collapse; }
  table.txns th { background: #146c43; color: white; padding: 6px 8px; text-align: left; font-size: 9pt; font-weight: 600; }
  table.txns th.num { text-align: right; }
  table.txns td { padding: 5px 8px; border-bottom: 1px solid #eee; font-size: 9.5pt; }
  table.txns td.num { text-align: right; font-family: 'Courier New', monospace; }
  .footer { margin-top: 26px; padding-top: 10px; border-top: 1px dashed #aaa; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div>
      <div class="name">{{tenant_name}}</div>
      <div class="addr">{{tenant_address}}</div>
    </div>
    <div class="right">
      <div style="font-weight:700;font-size:12pt;">SHARE STATEMENT</div>
      <div class="addr">Generated {{generated_date}}</div>
    </div>
  </div>
  <div class="meta">
    <div><div class="lbl">Member</div><div class="val">{{member_name}}</div><div class="addr">{{member_no}}</div></div>
    <div><div class="lbl">Period</div><div class="val">{{period_label}}</div></div>
  </div>
  <div class="summary">
    <div class="card"><div class="lbl">Closing shares</div><div class="val">{{closing_shares}}</div></div>
    <div class="card"><div class="lbl">Par value</div><div class="val">{{par_value}}</div></div>
    <div class="card"><div class="lbl">Total worth</div><div class="val">{{total_worth}}</div></div>
    <div class="card"><div class="lbl">Pledged</div><div class="val">{{pledged_shares}}</div></div>
  </div>
  <h1>Share transactions</h1>
  <table class="txns">
    <thead><tr>
      <th>Date</th><th>Ref</th><th>Type</th>
      <th class="num">Shares Δ</th><th class="num">Par</th><th class="num">Amount</th>
      <th class="num">Balance (shares)</th>
    </tr></thead>
    <tbody>{{transactions_html}}</tbody>
  </table>
  <div class="footer">{{tenant_disclaimer}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── 3. interest_statement ───────────

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'interest_statement', 1, 'Interest payout statement',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Interest statement</title>
<style>
  @page { size: A4; margin: 20mm 20mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 10pt; line-height: 1.45; }
  .head { border-bottom: 2px solid #8a6d00; padding-bottom: 10px; margin-bottom: 16px; display: flex; justify-content: space-between; align-items: flex-end; }
  .head .name { font-size: 18pt; color: #8a6d00; font-weight: 700; }
  .head .addr { color: #555; font-size: 8.5pt; margin-top: 3px; }
  .head .right { text-align: right; }
  .meta { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px; padding: 10px; background: #fff8e6; border-radius: 6px; margin-bottom: 16px; font-size: 9.5pt; }
  .meta .lbl { color: #666; font-size: 8.5pt; text-transform: uppercase; letter-spacing: 0.5px; }
  .meta .val { font-weight: 600; margin-top: 2px; }
  h1 { color: #8a6d00; font-size: 13pt; margin: 16px 0 10px; }
  table.lines { width: 100%; border-collapse: collapse; }
  table.lines th { background: #8a6d00; color: white; padding: 6px 8px; text-align: left; font-size: 9pt; font-weight: 600; }
  table.lines th.num { text-align: right; }
  table.lines td { padding: 5px 8px; border-bottom: 1px solid #eee; font-size: 9.5pt; }
  table.lines td.num { text-align: right; font-family: 'Courier New', monospace; }
  table.lines tr.totals { background: #fff4d6; font-weight: 700; }
  .footer { margin-top: 26px; padding-top: 10px; border-top: 1px dashed #aaa; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div>
      <div class="name">{{tenant_name}}</div>
      <div class="addr">{{tenant_address}}</div>
    </div>
    <div class="right">
      <div style="font-weight:700;font-size:12pt;">INTEREST STATEMENT</div>
      <div class="addr">Generated {{generated_date}}</div>
    </div>
  </div>
  <div class="meta">
    <div><div class="lbl">Member</div><div class="val">{{member_name}}</div><div class="addr">{{member_no}}</div></div>
    <div><div class="lbl">Financial year</div><div class="val">{{fy_label}}</div></div>
    <div><div class="lbl">AGM rate</div><div class="val">{{agm_rate_pct}}%</div></div>
    <div><div class="lbl">Run reference</div><div class="val">{{run_no}}</div></div>
    <div><div class="lbl">Run date</div><div class="val">{{run_date}}</div></div>
    <div><div class="lbl">WHT rate</div><div class="val">{{wht_rate_pct}}%</div></div>
  </div>
  <h1>Per-account interest</h1>
  <table class="lines">
    <thead><tr>
      <th>Account</th>
      <th class="num">Weighted avg balance</th><th class="num">Rate</th>
      <th class="num">Gross interest</th><th class="num">WHT</th><th class="num">Net</th>
      <th>Payout</th>
    </tr></thead>
    <tbody>{{lines_html}}</tbody>
  </table>
  <div class="footer">{{tenant_disclaimer}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;

-- ─────────── 4. dividend_statement ───────────

INSERT INTO pdf_templates (tenant_id, document_type, version_no, label, html_body, page_size)
SELECT t.id, 'dividend_statement', 1, 'Dividend payout statement',
$html$<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Dividend statement</title>
<style>
  @page { size: A4; margin: 20mm 20mm; }
  body { font-family: 'Helvetica Neue', Arial, sans-serif; color: #222; font-size: 10pt; line-height: 1.45; }
  .head { border-bottom: 2px solid #b42318; padding-bottom: 10px; margin-bottom: 16px; display: flex; justify-content: space-between; align-items: flex-end; }
  .head .name { font-size: 18pt; color: #b42318; font-weight: 700; }
  .head .addr { color: #555; font-size: 8.5pt; margin-top: 3px; }
  .head .right { text-align: right; }
  .meta { display: grid; grid-template-columns: 1fr 1fr 1fr; gap: 12px; padding: 10px; background: #fdf2f1; border-radius: 6px; margin-bottom: 16px; font-size: 9.5pt; }
  .meta .lbl { color: #666; font-size: 8.5pt; text-transform: uppercase; letter-spacing: 0.5px; }
  .meta .val { font-weight: 600; margin-top: 2px; }
  h1 { color: #b42318; font-size: 13pt; margin: 16px 0 10px; }
  table.summary { width: 100%; border-collapse: collapse; margin-bottom: 16px; }
  table.summary td { padding: 8px 10px; border-bottom: 1px dotted #ccc; font-size: 10pt; }
  table.summary td.lbl { color: #555; width: 60%; }
  table.summary td.amt { text-align: right; font-family: 'Courier New', monospace; }
  table.summary tr.totals { background: #fdf2f1; font-weight: 700; }
  table.summary tr.totals td { border-bottom: 2px solid #b42318; }
  .payout { padding: 10px; background: #f7f9fc; border-radius: 6px; margin-top: 14px; font-size: 10pt; }
  .footer { margin-top: 26px; padding-top: 10px; border-top: 1px dashed #aaa; text-align: center; font-size: 8pt; color: #666; }
</style></head><body>
  <div class="head">
    <div>
      <div class="name">{{tenant_name}}</div>
      <div class="addr">{{tenant_address}}</div>
    </div>
    <div class="right">
      <div style="font-weight:700;font-size:12pt;">DIVIDEND STATEMENT</div>
      <div class="addr">Generated {{generated_date}}</div>
    </div>
  </div>
  <div class="meta">
    <div><div class="lbl">Member</div><div class="val">{{member_name}}</div><div class="addr">{{member_no}}</div></div>
    <div><div class="lbl">Financial year</div><div class="val">{{fy_label}}</div></div>
    <div><div class="lbl">AGM rate</div><div class="val">{{agm_rate_pct}}%</div></div>
    <div><div class="lbl">Run reference</div><div class="val">{{run_no}}</div></div>
    <div><div class="lbl">Run date</div><div class="val">{{run_date}}</div></div>
    <div><div class="lbl">WHT rate</div><div class="val">{{wht_rate_pct}}%</div></div>
  </div>
  <h1>Dividend calculation</h1>
  <table class="summary">
    <tr><td class="lbl">Average share capital</td><td class="amt">{{average_share_capital}}</td></tr>
    <tr><td class="lbl">Shares basis</td><td class="amt">{{shares_basis}}</td></tr>
    <tr><td class="lbl">Gross dividend</td><td class="amt">{{gross_dividend}}</td></tr>
    <tr><td class="lbl">Less: Withholding tax @ {{wht_rate_pct}}%</td><td class="amt">− {{wht_amount}}</td></tr>
    <tr class="totals"><td class="lbl">Net dividend payable</td><td class="amt">{{net_dividend}}</td></tr>
  </table>
  <div class="payout">
    <strong>Payout:</strong> {{payout_method}}{{payout_destination_suffix}}
  </div>
  <div class="footer">{{tenant_disclaimer}}</div>
</body></html>$html$,
'A4'
  FROM tenants t
ON CONFLICT (tenant_id, document_type, version_no) DO NOTHING;
