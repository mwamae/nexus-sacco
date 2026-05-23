// /collect/receipts (today's receipts on my till) and
// /collect/receipts/:id (single receipt detail). Same file because
// they share types + the per-line status table.

import { useEffect, useState } from 'react';
import {
  extractError,
  getCurrentTillSession,
  getReceipt,
  listReceipts,
  receiptPDFDownloadURL,
  renderReceiptPDF,
  voidReceiptLine,
  type ApiReceipt,
  type ApiReceiptLine,
  type ReceiptLineKind,
} from '../api/client';

const LINE_KIND_LABELS: Record<ReceiptLineKind, string> = {
  savings_deposit: 'Savings deposit',
  share_purchase: 'Share purchase',
  loan_repayment: 'Loan repayment',
  fee: 'Fee payment',
  welfare: 'Welfare contribution',
};

export default function CollectionReceipts() {
  const path = window.location.pathname;
  // /collect/receipts/<id>
  const detailMatch = path.match(/^\/collect\/receipts\/([^/]+)\/?$/);
  if (detailMatch) {
    return <ReceiptDetail id={detailMatch[1]} />;
  }
  return <ReceiptsList />;
}

// ─────────── List ───────────

function ReceiptsList() {
  const today = new Date().toISOString().slice(0, 10);
  const [rows, setRows] = useState<ApiReceipt[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [tillCode, setTillCode] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const till = await getCurrentTillSession();
        let tillSessionId: string | undefined;
        if (till.has_open_session) {
          tillSessionId = till.session_id;
          setTillCode(till.till_code ?? null);
        }
        // If no open till, list everything for today by value_date
        // (still useful for shift handover reporting).
        const r = await listReceipts({
          till_session_id: tillSessionId,
          value_date: today,
        });
        setRows(r);
      } catch (e) {
        setErr(extractError(e));
      }
    })();
  }, [today]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Servicing · Collection Desk</div>
          <h1>Today's receipts</h1>
          <div className="page-sub">
            {tillCode ? <>Scoped to till <strong>{tillCode}</strong>.</> : 'No open till — showing all of today.'}
          </div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm btn-accent" href="/collect">+ New receipt</a>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}
      {rows === null && !err && <div className="empty">Loading…</div>}
      {rows && rows.length === 0 && (
        <div className="card">
          <div className="card-body">
            <div className="empty">No receipts on this till today yet.</div>
          </div>
        </div>
      )}
      {rows && rows.length > 0 && (
        <div className="card">
          <div className="card-hd">
            <h3>{rows.length} receipt{rows.length === 1 ? '' : 's'}</h3>
            <span className="card-sub">Total KES {sumChannelAmounts(rows).toFixed(2)}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Serial</th>
                  <th>Counterparty</th>
                  <th>Channel</th>
                  <th>Lines</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Amount</th>
                  <th>When</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny-mono">{r.serial}</td>
                    <td className="tiny-mono">{r.counterparty_id.slice(0, 8)}…</td>
                    <td>{r.channel}{r.channel_ref ? <span className="muted tiny"> · {r.channel_ref}</span> : null}</td>
                    <td className="tiny-mono">{(r.lines?.length ?? 0)}</td>
                    <td><span className="badge">{r.status}</span></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.channel_amount}</td>
                    <td className="tiny-mono">{r.created_at.slice(11, 19)}</td>
                    <td>
                      <a className="btn btn-sm" href={`/collect/receipts/${r.id}`}>Open →</a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

function sumChannelAmounts(rows: ApiReceipt[]): number {
  return rows.reduce((acc, r) => acc + (parseFloat(r.channel_amount) || 0), 0);
}

// ─────────── Detail ───────────

function ReceiptDetail({ id }: { id: string }) {
  const [receipt, setReceipt] = useState<ApiReceipt | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [voidingLineId, setVoidingLineId] = useState<string | null>(null);
  const [renderingPDF, setRenderingPDF] = useState(false);

  async function reload() {
    try {
      setReceipt(await getReceipt(id));
    } catch (e) {
      setErr(extractError(e));
    }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [id]);

  async function renderPDF() {
    setRenderingPDF(true);
    setErr(null);
    try {
      const out = await renderReceiptPDF(id);
      // Open the freshly-rendered PDF in a new tab so the cashier can
      // print without leaving the desk page.
      window.open(receiptPDFDownloadURL(out.pdf_document_id), '_blank', 'noopener');
      await reload();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setRenderingPDF(false);
    }
  }

  async function voidLine(line: ApiReceiptLine) {
    const reason = window.prompt(`Void reason for line ${line.line_no} (${line.kind})?`);
    if (!reason) return;
    setVoidingLineId(line.id);
    try {
      await voidReceiptLine(id, line.id, reason);
      await reload();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setVoidingLineId(null);
    }
  }

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!receipt) return <div className="page"><div className="empty">Loading…</div></div>;

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/collect/receipts" style={{ color: 'var(--muted)' }}>← Receipts</a></div>
          <h1 className="tiny-mono" style={{ fontFamily: 'inherit' }}>{receipt.serial}</h1>
          <div className="page-sub">
            <span className="badge">{receipt.status}</span>
            {' · '}{receipt.channel}{receipt.channel_ref ? ' · ' + receipt.channel_ref : ''}
            {' · '}KES {receipt.channel_amount}
            {' · '}{receipt.value_date}
          </div>
        </div>
        <div className="page-hd-actions">
          {receipt.pdf_document_id ? (
            <a
              className="btn btn-sm btn-accent"
              href={receiptPDFDownloadURL(receipt.pdf_document_id)}
              target="_blank"
              rel="noopener"
            >
              Download PDF
            </a>
          ) : (
            <button
              className="btn btn-sm btn-accent"
              onClick={renderPDF}
              disabled={renderingPDF}
            >
              {renderingPDF ? 'Rendering…' : 'Render PDF'}
            </button>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-hd"><h3>Lines</h3></div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>#</th>
                <th>Kind</th>
                <th>Target / fee</th>
                <th style={{ textAlign: 'right' }}>Amount</th>
                <th>Status</th>
                <th>Approval</th>
                <th>Posted txn</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(receipt.lines ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="tiny-mono">{l.line_no}</td>
                  <td>
                    {LINE_KIND_LABELS[l.kind]}
                    {l.narration && <div className="muted tiny">{l.narration}</div>}
                  </td>
                  <td className="tiny-mono">
                    {l.target_account_id ? l.target_account_id.slice(0, 8) + '…' : (l.fee_code ?? '—')}
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{l.amount}</td>
                  <td><span className="badge">{l.status}</span></td>
                  <td className="tiny-mono">
                    {l.approval_id
                      ? <a href={`/cash-approvals#${l.approval_id}`}>{l.approval_id.slice(0, 8)}…</a>
                      : <span className="muted">—</span>}
                  </td>
                  <td className="tiny-mono">
                    {l.posted_txn_id ? l.posted_txn_id.slice(0, 8) + '…' : <span className="muted">—</span>}
                  </td>
                  <td>
                    {(l.status === 'pending' || l.status === 'posted') && (
                      <button
                        className="btn btn-sm"
                        onClick={() => voidLine(l)}
                        disabled={voidingLineId === l.id}
                      >
                        {voidingLineId === l.id ? 'Voiding…' : 'Void'}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Metadata</h3></div>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, fontSize: 13 }}>
          <Meta label="Cashier" value={receipt.cashier_user_id.slice(0, 8) + '…'} mono />
          <Meta label="Counterparty" value={receipt.counterparty_id.slice(0, 8) + '…'} mono />
          <Meta label="Till session" value={receipt.till_session_id ? receipt.till_session_id.slice(0, 8) + '…' : '(virtual)'} mono />
          <Meta label="Virtual till" value={receipt.virtual_till_id ? receipt.virtual_till_id.slice(0, 8) + '…' : '(physical)'} mono />
          <Meta label="Created" value={receipt.created_at.slice(0, 19).replace('T', ' ')} mono />
          <Meta label="Posted" value={receipt.posted_at ? receipt.posted_at.slice(0, 19).replace('T', ' ') : '—'} mono />
        </div>
      </div>
    </div>
  );
}

function Meta({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div className={mono ? 'tiny-mono' : 'tiny'}>{value}</div>
    </div>
  );
}
