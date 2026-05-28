// My-queue page — the officer's assigned loans with DPD + last
// activity. Tap a card to expand quick-log actions.
//
// Offline mode is a follow-up: actions submitted while offline need
// IndexedDB queueing + a background sync. This scaffold requires
// connectivity for every action.

import { useEffect, useState } from 'react';
import { myQueue, logCall, logVisit, type QueueRow } from '../api';

function decodeOfficerID(): string {
  // Lazy JWT decode — just the payload. Real apps validate signature.
  const jwt = localStorage.getItem('officer_jwt');
  if (!jwt) return '';
  try {
    const payload = JSON.parse(atob(jwt.split('.')[1]));
    return payload.sub ?? payload.user_id ?? '';
  } catch { return ''; }
}

export default function Queue() {
  const officerID = decodeOfficerID();
  const [items, setItems] = useState<QueueRow[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);

  async function reload() {
    setErr(null);
    try {
      setItems(await myQueue(officerID));
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Failed to load queue');
    }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <h2 style={{ margin: 0 }}>My queue</h2>
        <button className="btn" onClick={() => void reload()}>↻</button>
      </div>
      {err && <div style={{ color: '#c33', fontSize: 13, marginBottom: 8 }}>{err}</div>}
      {items.length === 0 ? (
        <div className="card muted">No loans assigned to you.</div>
      ) : items.map((r) => (
        <LoanCard
          key={r.loan_id} row={r}
          expanded={expanded === r.loan_id}
          onToggle={() => setExpanded(expanded === r.loan_id ? null : r.loan_id)}
          onActionDone={() => void reload()}
        />
      ))}
    </div>
  );
}

function LoanCard({ row, expanded, onToggle, onActionDone }: {
  row: QueueRow; expanded: boolean; onToggle: () => void; onActionDone: () => void;
}) {
  return (
    <div className="card">
      <div onClick={onToggle} style={{ cursor: 'pointer' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between' }}>
          <strong className="mono">{row.loan_no}</strong>
          <span className={`badge ${row.dpd_days > 60 ? 'badge-danger' : row.dpd_days > 0 ? 'badge-warn' : ''}`}>
            DPD {row.dpd_days}
          </span>
        </div>
        <div style={{ marginTop: 4 }}>{row.member_name}</div>
        <div className="muted" style={{ marginTop: 4 }}>
          Outstanding {row.outstanding_total} · {row.classification}
        </div>
        {row.last_event_kind && (
          <div className="muted" style={{ marginTop: 2 }}>
            Last: {row.last_event_kind}{row.last_event_at && ` · ${new Date(row.last_event_at).toLocaleDateString()}`}
          </div>
        )}
      </div>
      {expanded && <QuickActions loanID={row.loan_id} onDone={onActionDone} />}
    </div>
  );
}

function QuickActions({ loanID, onDone }: { loanID: string; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function call(outcome: string) {
    setBusy(true); setErr(null);
    const note = window.prompt('Note (optional)') ?? '';
    try {
      await logCall(loanID, outcome, note);
      onDone();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Action failed');
    } finally { setBusy(false); }
  }
  async function visit(outcome: string) {
    setBusy(true); setErr(null);
    const note = window.prompt('Visit note') ?? '';
    let lat: string | undefined, lng: string | undefined;
    if ('geolocation' in navigator) {
      await new Promise<void>((resolve) => {
        navigator.geolocation.getCurrentPosition(
          (pos) => { lat = String(pos.coords.latitude); lng = String(pos.coords.longitude); resolve(); },
          () => resolve(),
          { timeout: 5000 },
        );
      });
    }
    try {
      await logVisit(loanID, outcome, note, lat, lng);
      onDone();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Action failed');
    } finally { setBusy(false); }
  }
  return (
    <div style={{ marginTop: 10, paddingTop: 10, borderTop: '1px solid #e2e8f0' }}>
      <div className="muted" style={{ marginBottom: 6 }}>Log call</div>
      <div className="btn-row">
        <button className="btn" disabled={busy} onClick={() => void call('reached_promised')}>Promised</button>
        <button className="btn" disabled={busy} onClick={() => void call('reached_refused')}>Refused</button>
        <button className="btn" disabled={busy} onClick={() => void call('no_answer')}>No answer</button>
        <button className="btn" disabled={busy} onClick={() => void call('voicemail')}>Voicemail</button>
      </div>
      <div className="muted" style={{ marginBottom: 6, marginTop: 10 }}>Log visit</div>
      <div className="btn-row">
        <button className="btn" disabled={busy} onClick={() => void visit('found_promised')}>Found · promised</button>
        <button className="btn" disabled={busy} onClick={() => void visit('not_found_home')}>Not home</button>
      </div>
      {err && <div style={{ color: '#c33', marginTop: 8, fontSize: 12 }}>{err}</div>}
    </div>
  );
}
