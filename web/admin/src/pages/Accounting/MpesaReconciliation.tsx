// /accounting/mpesa-reconciliation — staff-facing reconciliation
// surface. Lists inbound C2B events with their distribution status,
// resolved member, and a quick lens into the splits + GL state.
//
// Phase-5 scope:
//   • Date-range picker (defaults to today, UTC)
//   • Three KPIs: Inbound received / distributed / unallocated
//   • Status + paybill + msisdn + bill_ref filters
//   • Re-run distribution action for failed rows (admin gated)
//   • Statement-diff panel: stub — phase 6 wires the real Daraja
//     statement pull
//
// Permissions:
//   • tenant:settings:view — see the page + run reads
//   • tenant:settings:edit — re-run distribution action
// Lower-permission users see the table but no row actions.

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  listMpesaInboundEvents,
  listMpesaPaybills,
  extractError,
  type ApiMpesaInboundEvent,
  type ApiMpesaPaybill,
  type MpesaInboundStatus,
} from '../../api/client';
import { Badge } from '../../components/Badge';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

type StatusFilter = 'all' | MpesaInboundStatus;

export default function MpesaReconciliation() {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('tenant:settings:edit');
  useDocumentTitle('M-PESA reconciliation');

  const today = new Date().toISOString().slice(0, 10);
  const initialPaybill = useMemo<string | undefined>(() => {
    const v = new URLSearchParams(window.location.search).get('paybill_id');
    return v ?? undefined;
  }, []);

  const [from, setFrom] = useState(today);
  const [to, setTo] = useState(today);
  const [status, setStatus] = useState<StatusFilter>('all');
  const [paybillID, setPaybillID] = useState<string | undefined>(initialPaybill);
  const [msisdn, setMSISDN] = useState('');
  const [billRef, setBillRef] = useState('');

  const [paybills, setPaybills] = useState<ApiMpesaPaybill[]>([]);
  const [events, setEvents] = useState<ApiMpesaInboundEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listMpesaPaybills().then(setPaybills).catch(() => { /* leave empty */ });
  }, []);

  async function reload() {
    setErr(null); setBusy(true);
    try {
      const r = await listMpesaInboundEvents({
        from: dateToRFC3339(from),
        to:   dateToRFC3339(to, /*endOfDay*/ true),
        status: status === 'all' ? undefined : status,
        paybill_id: paybillID,
        msisdn: msisdn || undefined,
        bill_ref: billRef || undefined,
        limit: 200,
      });
      setEvents(r.events);
      setTotal(r.total);
    } catch (e) {
      setErr(extractError(e));
    } finally { setBusy(false); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [from, to, status, paybillID, msisdn, billRef]);

  // KPI math is over the *fetched window* (not the per-filter slice).
  // We re-fetch with status='all' for the counts row so chip
  // narrowing doesn't move the headlines.
  const [headline, setHeadline] = useState<{ received: number; distributed: number; failed: number; unallocated: number }>({
    received: 0, distributed: 0, failed: 0, unallocated: 0,
  });
  useEffect(() => {
    listMpesaInboundEvents({
      from: dateToRFC3339(from),
      to:   dateToRFC3339(to, true),
      paybill_id: paybillID,
      limit: 200,
    })
      .then((r) => {
        const received = r.events.filter((e) => e.status === 'received').length;
        const distributed = r.events.filter((e) => e.status === 'distributed').length;
        const failed = r.events.filter((e) => e.status === 'failed').length;
        const unallocated = r.events.filter((e) => e.resolved_via === 'unallocated').length;
        setHeadline({ received, distributed, failed, unallocated });
      })
      .catch(() => { /* leave zeros */ });
  }, [from, to, paybillID]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Accounting · Reconciliation</div>
          <h1>M-PESA reconciliation</h1>
          <div className="page-sub">
            Live view of Safaricom inbound traffic. Drill into a row to see splits + GL state.
            {!canEdit && ' — Read-only (no tenant:settings:edit).'}
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPICard label="Inbound (received)"  value={headline.received}  tone="warn" />
        <KPICard label="Distributed"         value={headline.distributed} tone="pos" />
        <KPICard label="Failed"              value={headline.failed}     tone="neg" />
        <KPICard label="Unallocated"         value={headline.unallocated} tone="warn" />
      </div>

      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny">From</div>
            <input type="date" className="input" value={from} onChange={(e) => setFrom(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">To</div>
            <input type="date" className="input" value={to} onChange={(e) => setTo(e.target.value)} />
          </label>
          <label>
            <div className="muted tiny">Paybill</div>
            <select className="input" value={paybillID ?? ''} onChange={(e) => setPaybillID(e.target.value || undefined)}>
              <option value="">All paybills</option>
              {paybills.map((p) => (
                <option key={p.id} value={p.id}>{p.shortcode} · {p.label}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny">MSISDN</div>
            <input className="input" value={msisdn} onChange={(e) => setMSISDN(e.target.value)} placeholder="254712345678" />
          </label>
          <label>
            <div className="muted tiny">Bill ref</div>
            <input className="input" value={billRef} onChange={(e) => setBillRef(e.target.value)} placeholder="M-2025-…" />
          </label>
          <div className="fchips">
            {(['all', 'received', 'distributed', 'failed'] as StatusFilter[]).map((s) => (
              <button
                key={s}
                type="button"
                className="fchip"
                data-active={status === s || undefined}
                onClick={() => setStatus(s)}
              >{s}</button>
            ))}
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-hd">
          <h3>Inbound events</h3>
          <span className="card-sub">{events.length} of {total} {busy && '· loading…'}</span>
        </div>
        <div className="card-body flush">
          {events.length === 0 && !busy && (
            <div className="empty">No inbound events for this filter.</div>
          )}
          {events.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Received</th>
                  <th>Tx ID</th>
                  <th>Bill ref</th>
                  <th>From</th>
                  <th style={{ textAlign: 'right' }}>Amount</th>
                  <th>Resolved</th>
                  <th>Status</th>
                  {canEdit && <th style={{ width: 1 }}></th>}
                </tr>
              </thead>
              <tbody>
                {events.map((e) => (
                  <tr key={e.id}>
                    <td className="tiny-mono">{new Date(e.received_at).toISOString().replace('T', ' ').slice(0, 19)}</td>
                    <td className="mono tiny">{e.transaction_id}</td>
                    <td className="mono tiny">{e.bill_ref || '—'}</td>
                    <td className="mono tiny">{e.msisdn || '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.amount}</td>
                    <td>
                      {e.resolved_via
                        ? <Badge tone={e.resolved_via === 'unallocated' ? 'warn' : 'neutral'}>{e.resolved_via}</Badge>
                        : <span className="muted tiny">—</span>}
                    </td>
                    <td><InboundStatusBadge status={e.status} /></td>
                    {canEdit && (
                      <td>
                        {e.status === 'failed' && (
                          <button className="btn btn-sm" title="Re-run distribution"
                            // The actual re-run endpoint isn't shipped yet
                            // (phase 6); this is a UI placeholder that
                            // disables itself + tells the operator how to
                            // recover via the workflow inbox today.
                            onClick={() => alert('Re-run distribution lands in phase 6 — for now resolve via the workflow inbox.')}>
                            Re-run
                          </button>
                        )}
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Statement diff</h3>
          <span className="card-sub">Daraja statement pull lands in phase 6</span>
        </div>
        <div className="card-body">
          <p className="muted tiny" style={{ marginTop: 0 }}>
            Once phase 6 ships, this panel will pull the paybill's
            Safaricom statement for the selected window and surface
            any transactions that don't have a matching{' '}
            <code>mpesa_inbound_events</code> row. For now the inbound
            table above is the source of truth.
          </p>
        </div>
      </div>
    </div>
  );
}

function KPICard({ label, value, tone }: { label: string; value: number; tone?: 'pos' | 'neg' | 'warn' }) {
  const color =
    tone === 'pos' ? 'var(--pos)' :
    tone === 'neg' ? 'var(--neg)' :
    tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
      </div>
    </div>
  );
}

function InboundStatusBadge({ status }: { status: MpesaInboundStatus }) {
  const tone = status === 'distributed' ? 'pos' : status === 'failed' ? 'neg' : 'warn';
  return <Badge tone={tone}>{status}</Badge>;
}

function dateToRFC3339(yyyymmdd: string, endOfDay = false): string {
  // The filter API accepts an RFC3339 timestamp; the date input
  // gives us YYYY-MM-DD. Anchor to 00:00:00Z (start) or 23:59:59Z
  // (end) so the "today only" default doesn't accidentally drop
  // events posted in the same day.
  const suffix = endOfDay ? 'T23:59:59Z' : 'T00:00:00Z';
  return yyyymmdd + suffix;
}
