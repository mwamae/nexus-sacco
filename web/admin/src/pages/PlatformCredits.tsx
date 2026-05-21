// Platform-admin Tenant Credit Management (Stage 9).
//
// Shows every tenant's balance, lets the platform admin top up, set
// pricing, fulfil tenant-submitted top-up requests, and view the full
// ledger per tenant. Also surfaces a platform-wide usage summary.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  getPlatformTenantDetail,
  listPlatformTenantBalances,
  platformFulfillTopupRequest,
  platformLedger,
  platformListTopupRequests,
  platformRejectTopupRequest,
  platformTopup,
  platformUpdatePricing,
  platformUsageSummary,
  type CreditBalance,
  type CreditChannel,
  type CreditLedgerEntry,
  type CreditPricing,
  type PlatformTenantBalance,
  type TopupRequest,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';

const CHANNELS: CreditChannel[] = ['sms', 'email'];
const CHANNEL_LABEL: Record<CreditChannel, string> = { sms: 'SMS', email: 'Email' };

type Tab = 'tenants' | 'requests' | 'analytics';

export default function PlatformCreditsPage() {
  const { user } = useAuth();
  const [tab, setTab] = useState<Tab>('tenants');
  const [tenants, setTenants] = useState<PlatformTenantBalance[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function loadTenants() {
    setErr(null);
    try {
      const r = await listPlatformTenantBalances();
      setTenants(r.items ?? []);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void loadTenants(); }, []);

  if (!user?.is_platform_admin) {
    return (
      <div className="page">
        <div className="empty">Platform-admin access required.</div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Platform · Notifications</div>
          <h1>Tenant credit management</h1>
          <div className="page-sub">
            Issue SMS + email credits, fulfil top-up requests, configure per-tenant pricing.
          </div>
        </div>
      </div>

      <div className="card" style={{ padding: 0 }}>
        <div className="tabs" style={{ padding: '0 14px' }}>
          {[
            { id: 'tenants' as const, label: 'Tenants' },
            { id: 'requests' as const, label: 'Top-up requests' },
            { id: 'analytics' as const, label: 'Analytics' },
          ].map((t) => (
            <div key={t.id} className="tab" data-active={tab === t.id || undefined} onClick={() => setTab(t.id)}>
              {t.label}
            </div>
          ))}
        </div>
        <div style={{ padding: 14 }}>
          {err && <div className="alert alert-error">{err}</div>}
          {tab === 'tenants' && (
            <TenantsTable
              tenants={tenants}
              onOpen={(id) => setSelectedID(id)}
              onRefresh={loadTenants}
            />
          )}
          {tab === 'requests' && <RequestsTable onChanged={loadTenants} />}
          {tab === 'analytics' && <AnalyticsPanel />}
        </div>
      </div>

      {selectedID && (
        <TenantDetailModal
          tenantID={selectedID}
          tenantName={tenants.find((t) => t.tenant_id === selectedID)?.name ?? ''}
          onClose={() => setSelectedID(null)}
          onChanged={loadTenants}
        />
      )}
    </div>
  );
}

// ─────────── Tenants table ───────────

function TenantsTable({
  tenants, onOpen, onRefresh,
}: {
  tenants: PlatformTenantBalance[];
  onOpen: (id: string) => void;
  onRefresh: () => void;
}) {
  if (tenants.length === 0) return <div className="empty">Loading…</div>;
  return (
    <>
      <div className="row" style={{ marginBottom: 8, gap: 8 }}>
        <button className="btn btn-sm btn-ghost" onClick={onRefresh}>Refresh</button>
      </div>
      <table className="tbl">
        <thead>
          <tr>
            <th>Tenant</th>
            <th>Slug</th>
            <th className="num">SMS balance</th>
            <th className="num">Email balance</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {tenants.map((t) => {
            const sms = t.balances.find((b) => b.channel === 'sms');
            const email = t.balances.find((b) => b.channel === 'email');
            const someZero = (sms && sms.balance < 1) || (email && email.balance < 1);
            const someLow = (sms && sms.balance > 0 && sms.low_balance_threshold > 0 && sms.balance <= sms.low_balance_threshold)
                         || (email && email.balance > 0 && email.low_balance_threshold > 0 && email.balance <= email.low_balance_threshold);
            return (
              <tr key={t.tenant_id}>
                <td><strong>{t.name}</strong></td>
                <td className="tiny" style={{ fontFamily: 'var(--font-mono)' }}>{t.slug}</td>
                <td className="num" style={{ color: (sms?.balance ?? 0) < 1 ? 'var(--neg)' : undefined }}>
                  {(sms?.balance ?? 0).toLocaleString()}
                </td>
                <td className="num" style={{ color: (email?.balance ?? 0) < 1 ? 'var(--neg)' : undefined }}>
                  {(email?.balance ?? 0).toLocaleString()}
                </td>
                <td>
                  {someZero ? <span style={{ color: 'var(--neg)', fontWeight: 600 }}>EXHAUSTED</span>
                   : someLow ? <span style={{ color: 'var(--warn)', fontWeight: 600 }}>LOW</span>
                   : <span style={{ color: 'var(--pos)' }}>OK</span>}
                </td>
                <td>
                  <button className="btn btn-sm btn-ghost" onClick={() => onOpen(t.tenant_id)}>Manage</button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </>
  );
}

// ─────────── Per-tenant detail modal ───────────

function TenantDetailModal({
  tenantID, tenantName, onClose, onChanged,
}: {
  tenantID: string;
  tenantName: string;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [detail, setDetail] = useState<{ balances: CreditBalance[]; pricing: CreditPricing[] } | null>(null);
  const [ledger, setLedger] = useState<CreditLedgerEntry[]>([]);
  const [view, setView] = useState<'top-up' | 'ledger' | 'pricing'>('top-up');
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const [d, l] = await Promise.all([
        getPlatformTenantDetail(tenantID),
        platformLedger(tenantID, { limit: 50 }),
      ]);
      setDetail(d);
      setLedger(l.items ?? []);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [tenantID]);

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 820, maxWidth: '94vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3 style={{ margin: 0 }}>{tenantName}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}>✕</button>
          </div>
        </div>
        <div className="card-body">
          {err && <div className="alert alert-error">{err}</div>}

          {detail && (
            <div className="row" style={{ gap: 16, flexWrap: 'wrap', marginBottom: 14 }}>
              {detail.balances.map((b) => (
                <div key={b.channel}>
                  <div className="muted tiny">{CHANNEL_LABEL[b.channel]} balance</div>
                  <div style={{ fontSize: 28, fontWeight: 700 }}>{b.balance.toLocaleString()}</div>
                </div>
              ))}
            </div>
          )}

          <div className="tabs" style={{ padding: 0, marginBottom: 10 }}>
            {[
              { id: 'top-up' as const, label: 'Top up' },
              { id: 'ledger' as const, label: 'Ledger' },
              { id: 'pricing' as const, label: 'Pricing' },
            ].map((v) => (
              <div key={v.id} className="tab" data-active={view === v.id || undefined} onClick={() => setView(v.id)}>
                {v.label}
              </div>
            ))}
          </div>

          {view === 'top-up' && (
            <TopupForm tenantID={tenantID} onCompleted={() => { void load(); onChanged(); }} />
          )}
          {view === 'ledger' && <LedgerTable entries={ledger} />}
          {view === 'pricing' && detail && (
            <PricingForm tenantID={tenantID} pricing={detail.pricing} onSaved={load} />
          )}
        </div>
      </div>
    </div>
  );
}

function TopupForm({ tenantID, onCompleted }: { tenantID: string; onCompleted: () => void }) {
  const [channel, setChannel] = useState<CreditChannel>('sms');
  const [credits, setCredits] = useState(100);
  const [reference, setReference] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setErr(null); setResult(null); setBusy(true);
    try {
      const r = await platformTopup(tenantID, { channel, credits, reference, notes });
      setResult(`Credited ${credits} ${CHANNEL_LABEL[channel]} credits. New balance: ${r.new_balance}`);
      onCompleted();
      setCredits(100); setReference(''); setNotes('');
    } catch (e) {
      setErr(extractErr(e));
    } finally { setBusy(false); }
  }

  return (
    <div className="form-grid">
      <Field label="Channel">
        <select value={channel} onChange={(e) => setChannel(e.target.value as CreditChannel)}>
          <option value="sms">SMS</option>
          <option value="email">Email</option>
        </select>
      </Field>
      <Field label="Credits to add">
        <input type="number" min={1} value={credits} onChange={(e) => setCredits(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Reference (PO / invoice)" hint="Recorded on the ledger row for reconciliation.">
        <input value={reference} onChange={(e) => setReference(e.target.value)} placeholder="INV-00234" style={{ width: '100%' }} />
      </Field>
      <Field label="Notes (optional)">
        <textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} style={{ width: '100%' }} />
      </Field>
      {err && <div className="alert alert-error">{err}</div>}
      {result && <div className="alert alert-info">{result}</div>}
      <button className="btn btn-primary" disabled={busy || credits <= 0} onClick={() => void submit()}>
        {busy ? 'Crediting…' : 'Credit account'}
      </button>
    </div>
  );
}

function LedgerTable({ entries }: { entries: CreditLedgerEntry[] }) {
  if (entries.length === 0) return <div className="empty">No movements yet.</div>;
  return (
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
        {entries.map((e) => (
          <tr key={e.id}>
            <td className="tiny">{new Date(e.created_at).toLocaleString()}</td>
            <td className="tiny">{CHANNEL_LABEL[e.channel]}</td>
            <td className="tiny" style={{ fontWeight: 600 }}>{e.movement_type}</td>
            <td className="num" style={{ color: e.credits < 0 ? 'var(--neg)' : 'var(--pos)' }}>
              {e.credits > 0 ? `+${e.credits}` : e.credits}
            </td>
            <td className="num">{e.balance_after}</td>
            <td className="tiny">
              {e.reference && <strong>{e.reference}</strong>}
              {e.reference && e.notes && ' · '}
              {e.notes}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function PricingForm({ tenantID, pricing, onSaved }: { tenantID: string; pricing: CreditPricing[]; onSaved: () => void }) {
  const byCh = useMemo(() => {
    const m: Record<string, CreditPricing> = {};
    for (const p of pricing) m[p.channel] = p;
    return m;
  }, [pricing]);
  return (
    <div>
      {CHANNELS.map((ch) => (
        <PricingRow
          key={ch}
          tenantID={tenantID}
          channel={ch}
          current={byCh[ch]}
          onSaved={onSaved}
        />
      ))}
    </div>
  );
}

function PricingRow({
  tenantID, channel, current, onSaved,
}: {
  tenantID: string;
  channel: CreditChannel;
  current?: CreditPricing;
  onSaved: () => void;
}) {
  const [price, setPrice] = useState(current?.price_per_credit ?? '0');
  const [currency, setCurrency] = useState(current?.currency_code ?? 'KES');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (current) {
      setPrice(current.price_per_credit);
      setCurrency(current.currency_code);
    }
  }, [current]);
  async function save() {
    setErr(null); setBusy(true);
    try {
      await platformUpdatePricing(tenantID, { channel, price_per_credit: price, currency_code: currency });
      onSaved();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(false); }
  }
  return (
    <div className="card" style={{ marginBottom: 10 }}>
      <div className="card-body">
        <div className="row" style={{ gap: 10, alignItems: 'flex-end' }}>
          <div style={{ minWidth: 90 }}>
            <div className="muted tiny">Channel</div>
            <div style={{ fontWeight: 600 }}>{CHANNEL_LABEL[channel]}</div>
          </div>
          <Field label="Price per credit">
            <input
              type="text"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              style={{ width: 100, fontFamily: 'var(--font-mono)' }}
            />
          </Field>
          <Field label="Currency">
            <input
              value={currency}
              onChange={(e) => setCurrency(e.target.value)}
              style={{ width: 70, fontFamily: 'var(--font-mono)' }}
            />
          </Field>
          <button className="btn btn-primary" disabled={busy} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
        {err && <div className="alert alert-error" style={{ marginTop: 6 }}>{err}</div>}
      </div>
    </div>
  );
}

// ─────────── Top-up requests tab ───────────

function RequestsTable({ onChanged }: { onChanged: () => void }) {
  const [items, setItems] = useState<Array<TopupRequest & { tenant_slug?: string; tenant_name?: string }>>([]);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await platformListTopupRequests({ status: 'pending' });
      setItems(r.items ?? []);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  async function fulfill(id: string) {
    const ref = prompt('Reference (invoice / payment id, optional):') ?? '';
    setBusy(id);
    try {
      await platformFulfillTopupRequest(id, { reference: ref });
      await load();
      onChanged();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(null); }
  }
  async function reject(id: string) {
    const reason = prompt('Rejection reason:') ?? '';
    if (!reason) return;
    setBusy(id);
    try {
      await platformRejectTopupRequest(id, reason);
      await load();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(null); }
  }

  if (err) return <div className="alert alert-error">{err}</div>;
  if (items.length === 0) return <div className="empty">No pending top-up requests.</div>;

  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Requested</th>
          <th>Tenant</th>
          <th>Channel</th>
          <th className="num">Credits</th>
          <th>Notes</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {items.map((t) => (
          <tr key={t.id}>
            <td className="tiny">{new Date(t.requested_at).toLocaleString()}</td>
            <td><strong>{t.tenant_name ?? '—'}</strong> <span className="muted tiny">{t.tenant_slug}</span></td>
            <td className="tiny">{CHANNEL_LABEL[t.channel]}</td>
            <td className="num">{t.credits_requested.toLocaleString()}</td>
            <td className="tiny">{t.notes ?? '—'}</td>
            <td>
              <button className="btn btn-sm btn-primary" disabled={busy === t.id} onClick={() => void fulfill(t.id)}>
                {busy === t.id ? '…' : 'Fulfill'}
              </button>
              <button className="btn btn-sm btn-ghost" disabled={busy === t.id} onClick={() => void reject(t.id)}>
                Reject
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ─────────── Analytics ───────────

function AnalyticsPanel() {
  const [data, setData] = useState<Awaited<ReturnType<typeof platformUsageSummary>> | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void (async () => {
      try { setData(await platformUsageSummary()); }
      catch (e) { setErr(extractErr(e)); }
    })();
  }, []);
  if (err) return <div className="alert alert-error">{err}</div>;
  if (!data) return <div className="empty">Loading…</div>;
  return (
    <div>
      <h3 style={{ marginTop: 0 }}>Totals (lifetime)</h3>
      <table className="tbl">
        <thead><tr><th>Channel</th><th className="num">Sold</th><th className="num">Consumed</th><th className="num">Remaining (sold − consumed)</th></tr></thead>
        <tbody>
          {data.totals.map((t) => (
            <tr key={t.channel}>
              <td>{t.channel.toUpperCase()}</td>
              <td className="num">{t.total_sold.toLocaleString()}</td>
              <td className="num">{t.total_consumed.toLocaleString()}</td>
              <td className="num">{(t.total_sold - t.total_consumed).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h3 style={{ marginTop: 16 }}>Tenants at zero balance</h3>
      {data.zero_balance_tenants.length === 0 ? (
        <div className="empty">All tenants have credits.</div>
      ) : (
        <table className="tbl">
          <thead><tr><th>Tenant</th><th>Channel</th><th className="num">Balance</th></tr></thead>
          <tbody>
            {data.zero_balance_tenants.map((z, i) => (
              <tr key={i}>
                <td>{z.slug}</td>
                <td className="tiny">{z.channel.toUpperCase()}</td>
                <td className="num" style={{ color: 'var(--neg)' }}>{z.balance}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ─────────── Tiny bits ───────────

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
