// Tenant-side Credits & Usage page (Stage 9).
//
// Replaces the per-tenant SMTP + SMS provider configuration screens
// from earlier stages. Tenants now see only:
//   • their two prepaid balances (SMS + email)
//   • per-credit pricing as set by the platform admin
//   • a paginated ledger of every credit movement
//   • a way to submit a top-up request for the platform to fulfil
//   • a low-balance threshold they can tune per channel
//   • a list of recently blocked deliveries with a Retry button

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  cancelTopupRequest,
  createTopupRequest,
  getCreditsLedger,
  getCreditsOverview,
  listBlockedDeliveries,
  retryBlockedDelivery,
  setLowBalanceThreshold,
  type CreditBalance,
  type CreditChannel,
  type CreditLedgerEntry,
  type CreditPricing,
  type CreditsOverview,
  type TopupRequest,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';

const CHANNELS: CreditChannel[] = ['sms', 'email'];
const CHANNEL_LABEL: Record<CreditChannel, string> = { sms: 'SMS', email: 'Email' };

export default function CreditsPage() {
  const { tenant } = useAuth();
  const [data, setData] = useState<CreditsOverview | null>(null);
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([]);
  const [ledgerLoading, setLedgerLoading] = useState(false);
  const [filterChannel, setFilterChannel] = useState<CreditChannel | ''>('');
  const [topupOpen, setTopupOpen] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function loadOverview() {
    setErr(null);
    try {
      const d = await getCreditsOverview();
      setData(d);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  async function loadLedger() {
    setLedgerLoading(true);
    try {
      const r = await getCreditsLedger({
        channel: filterChannel || undefined,
        limit: 50,
      });
      setLedger(r.items);
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setLedgerLoading(false);
    }
  }
  useEffect(() => { void loadOverview(); }, []);
  useEffect(() => { void loadLedger(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filterChannel]);

  const balanceByChannel = useMemo(() => {
    const m: Record<string, CreditBalance> = {};
    for (const b of data?.balances ?? []) m[b.channel] = b;
    return m;
  }, [data]);

  const priceByChannel = useMemo(() => {
    const m: Record<string, CreditPricing> = {};
    for (const p of data?.pricing ?? []) m[p.channel] = p;
    return m;
  }, [data]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Notifications</div>
          <h1>Credits &amp; Usage</h1>
          <div className="page-sub">
            Your SMS and email credits. The platform owns the providers; you pay per send
            from the prepaid balances below. In-app notifications are always free.
          </div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-primary" onClick={() => setTopupOpen(true)}>Request top-up</button>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {/* Balance summary cards */}
      <div className="row" style={{ gap: 12, flexWrap: 'wrap' }}>
        {CHANNELS.map((ch) => {
          const b = balanceByChannel[ch];
          const p = priceByChannel[ch];
          if (!b) return null;
          const zero = b.balance < 1;
          const low = !zero && b.low_balance_threshold > 0 && b.balance <= b.low_balance_threshold;
          return (
            <div key={ch} className="card" style={{ flex: '1 1 320px' }}>
              <div className="card-hd">
                <h3 style={{ margin: 0 }}>{CHANNEL_LABEL[ch]} credits</h3>
                <span className="card-sub" style={{
                  color: zero ? 'var(--neg)' : low ? 'var(--warn)' : 'var(--pos)',
                  fontWeight: 600,
                }}>
                  {zero ? 'EXHAUSTED' : low ? 'Low' : 'OK'}
                </span>
              </div>
              <div className="card-body">
                <div style={{ fontSize: 36, fontWeight: 700, lineHeight: 1 }}>
                  {b.balance.toLocaleString()}
                </div>
                <div className="muted tiny" style={{ marginTop: 4 }}>
                  credits available
                </div>
                {p && (
                  <div className="tiny" style={{ marginTop: 10 }}>
                    Rate: <strong>{p.currency_code} {p.price_per_credit}</strong> per credit
                  </div>
                )}
                {b.last_topup_at && (
                  <div className="muted tiny" style={{ marginTop: 4 }}>
                    Last top-up: {new Date(b.last_topup_at).toLocaleString()}
                    {b.last_topup_credits != null && ` (+${b.last_topup_credits})`}
                  </div>
                )}
                <ThresholdEditor channel={ch} initial={b.low_balance_threshold} onSaved={loadOverview} />
              </div>
            </div>
          );
        })}
      </div>

      {/* Pending top-up requests */}
      {data?.pending_topups && data.pending_topups.length > 0 && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="card-hd">
            <h3>Pending top-up requests</h3>
            <span className="card-sub">Awaiting platform admin fulfillment</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Requested</th>
                  <th>Channel</th>
                  <th className="num">Credits</th>
                  <th>Notes</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {data.pending_topups.map((t) => (
                  <tr key={t.id}>
                    <td className="tiny">{new Date(t.requested_at).toLocaleString()}</td>
                    <td className="tiny">{CHANNEL_LABEL[t.channel]}</td>
                    <td className="num">{t.credits_requested.toLocaleString()}</td>
                    <td className="tiny">{t.notes ?? '—'}</td>
                    <td>
                      <button
                        className="btn btn-sm btn-ghost"
                        onClick={async () => {
                          if (!confirm('Cancel this top-up request?')) return;
                          try { await cancelTopupRequest(t.id); await loadOverview(); }
                          catch (e) { setErr(extractErr(e)); }
                        }}
                      >
                        Cancel
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Ledger */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="card-hd">
          <h3>Usage history</h3>
          <div className="card-hd-actions">
            <select value={filterChannel} onChange={(e) => setFilterChannel(e.target.value as CreditChannel | '')}>
              <option value="">All channels</option>
              <option value="sms">SMS</option>
              <option value="email">Email</option>
            </select>
            <button className="btn btn-sm btn-ghost" onClick={() => void loadLedger()}>Refresh</button>
          </div>
        </div>
        <div className="card-body flush">
          {ledgerLoading && <div className="empty">Loading…</div>}
          {!ledgerLoading && ledger.length === 0 && <div className="empty">No movements yet.</div>}
          {!ledgerLoading && ledger.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Date</th>
                  <th>Channel</th>
                  <th>Type</th>
                  <th className="num">Credits</th>
                  <th className="num">Balance after</th>
                  <th>Reference / notes</th>
                </tr>
              </thead>
              <tbody>
                {ledger.map((e) => (
                  <tr key={e.id}>
                    <td className="tiny">{new Date(e.created_at).toLocaleString()}</td>
                    <td className="tiny">{CHANNEL_LABEL[e.channel]}</td>
                    <td className="tiny">
                      <span style={{
                        color: e.movement_type === 'topup' ? 'var(--pos)' :
                               e.movement_type === 'consumption' ? 'var(--fg-3)' :
                               'var(--accent)',
                        fontWeight: 600,
                      }}>{e.movement_type}</span>
                    </td>
                    <td className="num" style={{ color: e.credits < 0 ? 'var(--neg)' : 'var(--pos)' }}>
                      {e.credits > 0 ? `+${e.credits}` : e.credits}
                    </td>
                    <td className="num">{e.balance_after.toLocaleString()}</td>
                    <td className="tiny">
                      {e.reference && <strong>{e.reference}</strong>}
                      {e.reference && e.notes && ' · '}
                      {e.notes}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Blocked deliveries */}
      <BlockedDeliveriesPanel onChanged={loadOverview} />

      {topupOpen && (
        <TopupModal
          balances={data?.balances ?? []}
          pricing={data?.pricing ?? []}
          onClose={() => setTopupOpen(false)}
          onCreated={() => { setTopupOpen(false); void loadOverview(); }}
        />
      )}
    </div>
  );
}

// ─────────── Threshold editor ───────────

function ThresholdEditor({ channel, initial, onSaved }: { channel: CreditChannel; initial: number; onSaved: () => void }) {
  const [val, setVal] = useState(String(initial));
  const [busy, setBusy] = useState(false);
  useEffect(() => { setVal(String(initial)); }, [initial]);
  return (
    <div style={{ marginTop: 12, padding: 8, background: 'var(--surface-2)', borderRadius: 6 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>Low-balance warning at:</div>
      <div className="row" style={{ gap: 6, alignItems: 'center' }}>
        <input
          type="number"
          min={0}
          value={val}
          onChange={(e) => setVal(e.target.value)}
          style={{ width: 100 }}
        />
        <button
          className="btn btn-sm btn-ghost"
          disabled={busy || val === String(initial)}
          onClick={async () => {
            setBusy(true);
            try {
              await setLowBalanceThreshold(channel, Math.max(0, parseInt(val, 10) || 0));
              onSaved();
            } finally { setBusy(false); }
          }}
        >
          {busy ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  );
}

// ─────────── Top-up request modal ───────────

function TopupModal({
  balances, pricing, onClose, onCreated,
}: {
  balances: CreditBalance[];
  pricing: CreditPricing[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const [channel, setChannel] = useState<CreditChannel>('sms');
  const [credits, setCredits] = useState(100);
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const price = pricing.find((p) => p.channel === channel);
  const currentBalance = balances.find((b) => b.channel === channel)?.balance ?? 0;
  const estimatedCost = price ? (credits * parseFloat(price.price_per_credit)).toFixed(2) : null;

  async function submit() {
    setErr(null);
    setBusy(true);
    try {
      await createTopupRequest(channel, credits, notes);
      onCreated();
    } catch (e) {
      setErr(extractErr(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <ModalShell title="Request a top-up" onClose={onClose} width={520}>
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Submit a request — the platform admin will confirm payment and add credits to your balance.
      </p>
      <Field label="Channel">
        <select value={channel} onChange={(e) => setChannel(e.target.value as CreditChannel)} style={{ width: '100%' }}>
          <option value="sms">SMS (current balance: {currentBalance.toLocaleString()})</option>
          <option value="email">Email (current balance: {balances.find((b) => b.channel === 'email')?.balance ?? 0})</option>
        </select>
      </Field>
      <Field label="Credits requested">
        <input
          type="number"
          min={1}
          value={credits}
          onChange={(e) => setCredits(parseInt(e.target.value, 10) || 1)}
          style={{ width: '100%' }}
        />
        {estimatedCost !== null && price && (
          <div className="muted tiny" style={{ marginTop: 4 }}>
            Estimated cost: <strong>{price.currency_code} {estimatedCost}</strong> (at {price.price_per_credit} per credit)
          </div>
        )}
      </Field>
      <Field label="Notes (optional)" hint="Include PO / invoice ref if available.">
        <textarea
          rows={3}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          style={{ width: '100%' }}
        />
      </Field>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
        <button className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn btn-primary" disabled={busy || credits <= 0} onClick={() => void submit()}>
          {busy ? 'Submitting…' : 'Submit request'}
        </button>
      </div>
    </ModalShell>
  );
}

// ─────────── Blocked deliveries ───────────

function BlockedDeliveriesPanel({ onChanged }: { onChanged: () => void }) {
  const [smsItems, setSMS] = useState<string[]>([]);
  const [emailItems, setEmail] = useState<string[]>([]);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    try {
      const [s, e] = await Promise.all([
        listBlockedDeliveries('sms'),
        listBlockedDeliveries('email'),
      ]);
      setSMS(s.items ?? []);
      setEmail(e.items ?? []);
    } catch { /* swallow */ }
  }
  useEffect(() => { void load(); }, []);

  async function retry(id: string) {
    setBusy(id);
    try {
      await retryBlockedDelivery(id);
      await load();
      onChanged();
    } finally { setBusy(null); }
  }

  const total = smsItems.length + emailItems.length;
  if (total === 0) return null;

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="card-hd">
        <h3>Blocked deliveries</h3>
        <span className="card-sub">
          Notifications that didn't send because credits were exhausted. Top up the relevant
          channel and click Retry to re-queue them (24h window).
        </span>
      </div>
      <div className="card-body flush">
        <table className="tbl">
          <thead><tr><th>Channel</th><th>Delivery ID</th><th></th></tr></thead>
          <tbody>
            {smsItems.map((id) => (
              <tr key={id}>
                <td className="tiny">SMS</td>
                <td className="tiny" style={{ fontFamily: 'var(--font-mono)' }}>{id}</td>
                <td>
                  <button className="btn btn-sm" disabled={busy === id} onClick={() => void retry(id)}>
                    {busy === id ? 'Retrying…' : 'Retry'}
                  </button>
                </td>
              </tr>
            ))}
            {emailItems.map((id) => (
              <tr key={id}>
                <td className="tiny">Email</td>
                <td className="tiny" style={{ fontFamily: 'var(--font-mono)' }}>{id}</td>
                <td>
                  <button className="btn btn-sm" disabled={busy === id} onClick={() => void retry(id)}>
                    {busy === id ? 'Retrying…' : 'Retry'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ─────────── Bits ───────────

function ModalShell({ title, children, onClose, width }: { title: string; children: ReactNode; onClose: () => void; width?: number }) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: width ?? 560, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3 style={{ margin: 0 }}>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}>✕</button>
          </div>
        </div>
        <div className="card-body">{children}</div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
