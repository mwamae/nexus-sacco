// Guarantorships — member-portal self-service consent.
//
// Lists every loan the signed-in member has been asked to guarantee.
// Pending requests get Accept / Decline buttons; past responses
// render as read-only history.
//
// Calls /v1/portal/guarantorships + /respond. The savings backend
// bridges the JWT user → counterparty via email/phone match and
// refuses to return / mutate guarantees that don't belong to the
// bridged member.

import { useEffect, useState } from 'react';
import { api } from '../api';

type Row = {
  id: string;
  application_id: string;
  application_no: string;
  borrower_name: string;
  borrower_member_no: string;
  amount_guaranteed: string;
  requested_amount: string;
  product_name: string;
  status: string;
  requested_at: string;
  responded_at?: string;
  decline_reason?: string;
};

async function fetchList(): Promise<Row[]> {
  const r = await api.get('/v1/portal/guarantorships');
  return r.data?.data?.items ?? [];
}

async function respond(id: string, accept: boolean, declineReason?: string): Promise<void> {
  await api.post(`/v1/portal/guarantorships/${id}/respond`, {
    accept, decline_reason: declineReason,
  });
}

export default function Guarantorships() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function reload() {
    setErr(null);
    try {
      setRows(await fetchList());
    } catch (e: any) {
      const msg = e?.response?.data?.error?.message ?? e?.message ?? 'Failed to load guarantorships';
      setErr(msg);
    }
  }
  useEffect(() => { void reload(); }, []);

  async function onAccept(id: string) {
    if (!confirm('Confirm you accept this guarantee request? This is a legally binding commitment.')) return;
    setBusy(id);
    try {
      await respond(id, true);
      await reload();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Action failed');
    } finally {
      setBusy(null);
    }
  }

  async function onDecline(id: string) {
    const reason = window.prompt('Reason for declining (required):');
    if (!reason || !reason.trim()) return;
    setBusy(id);
    try {
      await respond(id, false, reason.trim());
      await reload();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message ?? 'Action failed');
    } finally {
      setBusy(null);
    }
  }

  const pending = (rows ?? []).filter((r) => r.status === 'pending_consent');
  const past = (rows ?? []).filter((r) => r.status !== 'pending_consent');

  return (
    <div>
      <div className="card">
        <h2 style={{ marginTop: 0 }}>Guarantee requests</h2>
        <p className="muted tiny">
          Loans where another member has asked you to act as guarantor.
          Accepting commits a portion of your savings as security — read
          the request carefully before responding.
        </p>
        {err && (
          <div style={{ background: '#ffeaea', border: '1px solid #f0a0a0', padding: 10, borderRadius: 4, fontSize: 13, marginTop: 8 }}>
            {err}
          </div>
        )}
      </div>

      {rows === null && <div className="card muted">Loading…</div>}

      {rows !== null && (
        <>
          <div className="card">
            <h3 style={{ marginTop: 0 }}>Pending response ({pending.length})</h3>
            {pending.length === 0 ? (
              <div className="muted tiny">Nothing waiting on you right now.</div>
            ) : pending.map((r) => (
              <div key={r.id} style={{ borderTop: '1px solid #e5e5e5', paddingTop: 12, marginTop: 12 }}>
                <div><strong>{r.borrower_name}</strong> <span className="muted">· {r.borrower_member_no}</span></div>
                <div className="row" style={{ marginTop: 6 }}>
                  <span className="lbl">Product</span><span>{r.product_name}</span>
                </div>
                <div className="row">
                  <span className="lbl">Application</span><span className="mono">{r.application_no}</span>
                </div>
                <div className="row">
                  <span className="lbl">Requested loan</span><span className="mono">KES {r.requested_amount}</span>
                </div>
                <div className="row">
                  <span className="lbl"><strong>Your guarantee</strong></span>
                  <span className="mono"><strong>KES {r.amount_guaranteed}</strong></span>
                </div>
                <div className="row">
                  <span className="lbl">Requested on</span>
                  <span>{new Date(r.requested_at).toLocaleDateString()}</span>
                </div>
                <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                  <button
                    className="btn btn-primary"
                    disabled={busy === r.id}
                    onClick={() => void onAccept(r.id)}
                  >
                    {busy === r.id ? 'Saving…' : 'Accept'}
                  </button>
                  <button
                    className="btn"
                    disabled={busy === r.id}
                    onClick={() => void onDecline(r.id)}
                  >
                    Decline
                  </button>
                </div>
              </div>
            ))}
          </div>

          {past.length > 0 && (
            <div className="card">
              <h3 style={{ marginTop: 0 }}>Past responses ({past.length})</h3>
              {past.map((r) => (
                <div key={r.id} style={{ borderTop: '1px solid #e5e5e5', paddingTop: 8, marginTop: 8 }}>
                  <div>
                    <strong>{r.borrower_name}</strong>
                    <span className="muted"> · KES {r.amount_guaranteed}</span>
                    <span style={{
                      marginLeft: 8, padding: '2px 8px', borderRadius: 4, fontSize: 11,
                      background: r.status === 'accepted' ? '#d4edda'
                        : r.status === 'declined' ? '#f8d7da'
                        : r.status === 'released' ? '#e2e3e5' : '#fff3cd',
                      color: '#333',
                    }}>
                      {r.status}
                    </span>
                  </div>
                  {r.responded_at && (
                    <div className="muted tiny" style={{ marginTop: 2 }}>
                      Responded {new Date(r.responded_at).toLocaleDateString()}
                      {r.decline_reason && ` · "${r.decline_reason}"`}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
