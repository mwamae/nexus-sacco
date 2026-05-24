// Fiscal year-end close.
//
// Posts the closing journal that zeros every income/expense account
// into Retained Earnings (3010) and locks every monthly period in the
// year. Once a year is closed it cannot be re-opened from this UI —
// that requires posting a manual reversal and is admin-only.

import { useEffect, useState } from 'react';
import {
  closeFiscalYear,
  getInboxStatus,
  listFiscalYearCloses,
  submitFiscalYearForClose,
  type FiscalYearClose,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function FiscalYearClosePage() {
  const { tenant } = useAuth();
  const currentYear = new Date().getFullYear();
  const [year, setYear] = useState(currentYear - 1);
  const [notes, setNotes] = useState('');
  const [items, setItems] = useState<FiscalYearClose[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);
  // Unified Inbox (PR #6): when on, the close is gated by Board
  // approval — the button becomes "Submit for close approval" and
  // the actual close fires from the workflow callback.
  const [inboxEnabled, setInboxEnabled] = useState(false);
  const [submittedWFID, setSubmittedWFID] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try { setItems((await listFiscalYearCloses()).items); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); }, []);
  useEffect(() => {
    getInboxStatus().then((s) => setInboxEnabled(s.unified_inbox_enabled)).catch(() => {});
  }, []);

  async function doClose() {
    if (!confirm(`Close fiscal year ${year}? This posts the year-end journal entry and locks every monthly period in ${year}. This action is one-shot — re-opening requires posting a manual reversal.`)) return;
    setErr(null); setInfo(null); setBusy(true);
    try {
      const r = await closeFiscalYear(year, notes || undefined);
      setInfo(`FY ${r.year} closed — net surplus ${r.net_surplus}, journal ${r.closing_entry_id.slice(0, 8)}…`);
      setNotes('');
      await load();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doSubmitForClose() {
    if (!confirm(`Submit fiscal year ${year} for Board approval? The close itself only fires after the Inbox approval lands.`)) return;
    setErr(null); setInfo(null); setBusy(true);
    try {
      const r = await submitFiscalYearForClose(year, notes || undefined);
      setSubmittedWFID(r.workflow_instance_id);
      setInfo(`FY ${year} submitted for Board approval (${r.status === 'existing' ? 'existing proposal' : 'new proposal'}).`);
      setNotes('');
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const alreadyClosed = items.some((i) => i.year === year);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Period close</div>
          <h1>Year-end close</h1>
          <div className="page-sub">
            Closes a fiscal year by posting the journal that rolls every income and expense account
            into Retained Earnings (3010), then locks every monthly period for the year.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {info && (
        <div className="alert" style={{ marginTop: 12, background: 'var(--pos-bg, #e6f5ea)', borderColor: 'var(--pos)' }}>
          {info}
          {submittedWFID && (
            <a className="btn btn-sm btn-accent" href={`/approvals/${submittedWFID}`} style={{ marginLeft: 8 }}>
              Open in Inbox →
            </a>
          )}
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Close a year</h3></div>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny">Fiscal year</div>
            <input
              type="number" min={2000} max={currentYear}
              value={year} onChange={(e) => setYear(parseInt(e.target.value || String(currentYear - 1), 10))}
            />
          </label>
          <label style={{ flex: 1, minWidth: 240 }}>
            <div className="muted tiny">Notes (optional)</div>
            <input type="text" value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="e.g. signed off by board on 2026-03-15" />
          </label>
          {inboxEnabled ? (
            <button
              className="btn btn-primary"
              disabled={busy || alreadyClosed}
              onClick={() => void doSubmitForClose()}
              title={alreadyClosed ? 'This year is already closed' : 'Sends to Board approval; close fires on approval'}
            >
              {busy ? 'Submitting…' : alreadyClosed ? 'Already closed' : `Submit FY ${year} for close approval →`}
            </button>
          ) : (
            <button
              className="btn btn-primary"
              disabled={busy || alreadyClosed}
              onClick={() => void doClose()}
              title={alreadyClosed ? 'This year is already closed' : undefined}
            >
              {busy ? 'Closing…' : alreadyClosed ? 'Already closed' : `Close FY ${year}`}
            </button>
          )}
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Closure history</h3>
          <span className="card-sub">{items.length} year{items.length === 1 ? '' : 's'} closed</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Year</th>
                <th>Period</th>
                <th className="num">Income</th>
                <th className="num">Expense</th>
                <th className="num">Net surplus</th>
                <th>Closed</th>
              </tr>
            </thead>
            <tbody>
              {items.map((i) => (
                <tr key={i.id}>
                  <td className="mono">{i.year}</td>
                  <td>{i.fy_start.slice(0, 10)} → {i.fy_end.slice(0, 10)}</td>
                  <td className="num mono">{i.total_income}</td>
                  <td className="num mono">{i.total_expense}</td>
                  <td className="num mono" style={{
                    fontWeight: 700,
                    color: parseFloat(i.net_surplus) >= 0 ? 'var(--pos)' : 'var(--neg)',
                  }}>
                    {i.net_surplus}
                  </td>
                  <td className="tiny muted">{new Date(i.closed_at).toLocaleString()}</td>
                </tr>
              ))}
              {items.length === 0 && (
                <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 18 }}>No years closed yet.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
