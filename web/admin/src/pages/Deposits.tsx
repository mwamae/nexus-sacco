// Deposits overview — tenant-wide register, summary by product,
// search + filter, deep-link to per-member statement.

import { useEffect, useState } from 'react';
import {
  extractError,
  getDepositsSummary,
  listDepositAccounts,
  listDepositProducts,
  type DepositAcctListItem,
  type DepositProduct,
  type DepositsSummary,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Avatar } from '../components/Avatar';
import { StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

export default function Deposits() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const [summary, setSummary] = useState<DepositsSummary | null>(null);
  const [products, setProducts] = useState<DepositProduct[]>([]);
  const [items, setItems] = useState<DepositAcctListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const [statusF, setStatusF] = useState('');
  const [productF, setProductF] = useState('');

  async function reload() {
    setErr(null);
    try {
      const [s, ps, l] = await Promise.all([
        getDepositsSummary(),
        listDepositProducts(true),
        listDepositAccounts({
          q: q || undefined,
          status: statusF || undefined,
          product_id: productF || undefined,
          limit: 100,
        }),
      ]);
      setSummary(s); setProducts(ps);
      setItems(l.items ?? []); setTotal(l.total ?? 0);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [statusF, productF]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Liabilities · Savings</div>
          <h1>Deposits</h1>
          <div className="page-sub">Member deposit accounts across all products.</div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href="/deposit-products"><Icon name="settings" size={12} /> Products</a>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {summary && (
        <div className="grid-4" style={{ marginBottom: 14 }}>
          <KPI label="Total deposits" value={`${currency} ${fmtMoney(summary.total_balance)}`} sub={`${summary.total_accounts} accounts`} />
          <KPI label="Active accounts" value={String(summary.active_accounts)} />
          <KPI label="Dormant accounts" value={String(summary.dormant_accounts)} tone={summary.dormant_accounts > 0 ? 'warn' : undefined} />
          <KPI label="Products configured" value={String(summary.by_product.length)} />
        </div>
      )}

      {summary && summary.by_product.length > 0 && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd">
            <h3>By product</h3>
            <span className="card-sub">Active balances</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Product</th>
                  <th>Type</th>
                  <th style={{ textAlign: 'right' }}>Active accounts</th>
                  <th style={{ textAlign: 'right' }}>Total balance</th>
                </tr>
              </thead>
              <tbody>
                {summary.by_product.map((p) => (
                  <tr key={p.product_id}>
                    <td>
                      <a href={`#filter-${p.product_id}`} className="tbl-link" onClick={(e) => { e.preventDefault(); setProductF(p.product_id); }}>
                        {p.name}
                      </a>
                      <div className="tiny-mono muted">{p.code}</div>
                    </td>
                    <td className="tiny">{p.product_type}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{p.active_accounts}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmtMoney(p.total_balance)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="card">
        <div className="card-hd">
          <h3>Accounts</h3>
          <span className="card-sub">{total.toLocaleString()} match{total === 1 ? '' : 'es'}</span>
          <div className="card-hd-actions">
            <form onSubmit={(e) => { e.preventDefault(); void reload(); }} style={{ display: 'flex', gap: 4 }}>
              <input
                className="input"
                style={{ height: 26, fontSize: 12, width: 220 }}
                placeholder="Search member / account #"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
            <select className="input" style={{ height: 26, fontSize: 12 }} value={productF} onChange={(e) => setProductF(e.target.value)}>
              <option value="">All products</option>
              {products.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <select className="input" style={{ height: 26, fontSize: 12 }} value={statusF} onChange={(e) => setStatusF(e.target.value)}>
              <option value="">All statuses</option>
              {['active', 'pending', 'dormant', 'matured', 'suspended', 'closed'].map((s) => <option key={s} value={s}>{s}</option>)}
            </select>
          </div>
        </div>
        <div className="card-body flush">
          {items.length === 0 ? (
            <div className="empty">No accounts match.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 44 }}></th>
                  <th>Member</th>
                  <th>Account</th>
                  <th>Product</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Balance</th>
                  <th>Last activity</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.account.id}>
                    <td><Avatar name={it.full_name} size="sm" /></td>
                    <td>
                      <div style={{ fontWeight: 500 }}>
                        <a href={`/members/${it.account.member_id}?tab=accounts`} className="tbl-link">{it.full_name}</a>
                      </div>
                      <div className="tiny-mono">{it.member_no}</div>
                    </td>
                    <td className="tiny-mono">{it.account.account_no}</td>
                    <td>
                      {it.product.name}
                      <div className="muted tiny">{it.product.product_type}</div>
                    </td>
                    <td><StatusBadge status={it.account.status} /></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmtMoney(it.account.current_balance)}</td>
                    <td className="tiny-mono">{it.account.last_activity_at ? it.account.last_activity_at.slice(0, 10) : '—'}</td>
                    <td>
                      <a className="btn btn-sm" href={`/members/${it.account.member_id}?tab=accounts`} title="Open"><Icon name="eye" size={12} /></a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
    </div>
  );
}

function fmtMoney(s: string): string {
  const n = parseFloat(s);
  if (!isFinite(n)) return s;
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
