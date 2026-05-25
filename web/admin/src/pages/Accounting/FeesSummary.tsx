// Fees & Collections Summary report.
//
// "Yesterday's fees, by code and by channel, with the GL accounts
// they hit" — the finance team's day-to-day reconciliation question.
// Same payload also backs the Member Profile → Fees tab (scoped via
// counterparty_id).
//
// The Unposted block surfaces fee/welfare receipt lines whose GL post
// never landed within 5 minutes — typically a brief accounting-service
// outage. The replay path is intentionally a manual jump to ops tools
// for now; one-click replay needs the outbox row id which this report
// doesn't precompute (the narration-LIKE join is too brittle).

import { useEffect, useState } from 'react';
import { feesSummary, downloadReport, type FeesSummary, type FeesSummaryFilter } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const CHANNELS = ['', 'cash', 'mpesa', 'airtel_money', 'bank_transfer', 'cheque', 'standing_order'];

export default function FeesSummaryPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const monthStart = today.slice(0, 8) + '01';
  const [from, setFrom] = useState(monthStart);
  const [to, setTo] = useState(today);
  const [channel, setChannel] = useState('');
  const [feeCode, setFeeCode] = useState('');
  const [data, setData] = useState<FeesSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try {
      const f: FeesSummaryFilter = { from, to };
      if (channel) f.channel = channel;
      if (feeCode) f.fee_code = feeCode;
      setData(await feesSummary(f));
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'failed to load');
    } finally {
      setBusy(false);
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reports</div>
          <h1>Fees &amp; Collections Summary</h1>
          <div className="page-sub">
            What fees were collected, by code and by channel, in this window — and which GL income accounts they hit.
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny">From</div>
            <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">To</div>
            <input type="date" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">Channel</div>
            <select value={channel} onChange={(e) => setChannel(e.target.value)}>
              {CHANNELS.map((c) => <option key={c} value={c}>{c || 'All channels'}</option>)}
            </select>
          </label>
          <label>
            <div className="muted tiny">Fee code</div>
            <input
              type="text"
              placeholder="all codes"
              value={feeCode}
              onChange={(e) => setFeeCode(e.target.value)}
              style={{ width: 180 }}
            />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Run report'}
          </button>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
            <button
              className="btn"
              disabled={!data}
              onClick={() => void downloadReport('fees-summary', {
                from, to,
                ...(channel ? { channel } : {}),
                ...(feeCode ? { fee_code: feeCode } : {}),
              })}
            >
              Export XLSX
            </button>
            <button className="btn" onClick={() => window.print()}>Print</button>
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {data && <FeesSummaryView data={data} />}
    </div>
  );
}

// FeesSummaryView renders the totals + by-code + by-channel + unposted
// blocks. Exported so the Member Profile Fees tab can reuse it.
export function FeesSummaryView({ data }: { data: FeesSummary }) {
  return (
    <>
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
          <Stat label="Total amount" value={data.total_amount} />
          <Stat label="Voided amount" value={data.total_voided} />
          <Stat label="Net amount" value={data.net_amount} bold />
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>By fee code</h3></div>
        <div className="card-body flush">
          {data.by_fee_code.length === 0 ? (
            <div className="empty" style={{ padding: 20 }}>No fees collected in the window.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Label</th>
                  <th>GL credit</th>
                  <th className="num">Count</th>
                  <th className="num">Total</th>
                  <th className="num">Voided</th>
                  <th className="num">Net</th>
                </tr>
              </thead>
              <tbody>
                {data.by_fee_code.map((r) => (
                  <tr key={r.fee_code}>
                    <td className="mono">{r.fee_code || '—'}</td>
                    <td>{r.fee_label}</td>
                    <td>
                      {r.gl_credit_code ? (
                        <a className="tbl-link" href={`/accounting/journal-entries?account=${r.gl_credit_code}`}>
                          {r.gl_credit_code}
                        </a>
                      ) : '—'}
                    </td>
                    <td className="num">{r.count}</td>
                    <td className="num mono">{r.total_amount}</td>
                    <td className="num mono">{r.voided_amount}</td>
                    <td className="num mono"><strong>{r.net_amount}</strong></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>By channel</h3></div>
        <div className="card-body flush">
          {data.by_channel.length === 0 ? (
            <div className="empty" style={{ padding: 20 }}>No fees collected in the window.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Channel</th>
                  <th className="num">Count</th>
                  <th className="num">Total</th>
                  <th className="num">Voided</th>
                  <th className="num">Net</th>
                </tr>
              </thead>
              <tbody>
                {data.by_channel.map((r) => (
                  <tr key={r.channel}>
                    <td>{r.channel}</td>
                    <td className="num">{r.count}</td>
                    <td className="num mono">{r.total_amount}</td>
                    <td className="num mono">{r.voided_amount}</td>
                    <td className="num mono"><strong>{r.net_amount}</strong></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {data.unposted.length > 0 && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd">
            <h3>Unposted lines</h3>
            <span className="card-sub">
              Fee/welfare lines awaiting GL post for more than 5 minutes — likely an accounting-service outage.
              Investigate via the posting outbox; on-call can replay stuck rows from there.
            </span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Receipt</th>
                  <th>Line</th>
                  <th>Fee code</th>
                  <th>Channel</th>
                  <th className="num">Amount</th>
                  <th className="num">Age (min)</th>
                </tr>
              </thead>
              <tbody>
                {data.unposted.map((u) => (
                  <tr key={u.line_id}>
                    <td className="mono">
                      <a className="tbl-link" href={`/collect/receipts/${u.receipt_id}`}>{u.receipt_serial}</a>
                    </td>
                    <td className="mono tiny">{u.line_id.slice(0, 8)}…</td>
                    <td className="mono">{u.fee_code || '—'}</td>
                    <td>{u.channel}</td>
                    <td className="num mono">{u.amount}</td>
                    <td className="num">{u.age_minutes}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </>
  );
}

function Stat({ label, value, bold }: { label: string; value: string; bold?: boolean }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div className="mono" style={{ fontSize: 20, fontWeight: bold ? 700 : 400 }}>{value}</div>
    </div>
  );
}
