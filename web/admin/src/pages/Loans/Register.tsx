// /loans/register — Loans Register (Phase 1).
//
// The single browsable list of every loan in the SACCO. Filters
// cover the daily ops needs: status, DPD bucket, product, free-text
// search, date ranges.
//
// DPD bucketing is computed inline for Phase 1 (next_due_date < now()
// = "1plus"; further refinement waits for Phase 3's real DPD engine).
//
// Row click → /loans/register/{id} for the consolidated loan
// workspace.
//
// Permission: loans:view.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  listLoans,
  listLoanProducts,
  type LoanListItem,
  type LoanProduct,
  extractError,
} from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const STATUS_LABEL: Record<string, string> = {
  pending_disbursement: 'Pending disbursement',
  active: 'Active',
  in_arrears: 'In arrears',
  defaulted: 'Defaulted',
  restructured: 'Restructured',
  settled: 'Settled',
  written_off: 'Written off',
  closed: 'Closed',
};

const DPD_BUCKETS = [
  { id: '',       label: 'Any DPD' },
  { id: 'current', label: 'Current (0)' },
  { id: '1plus',   label: '1+ days overdue' },
] as const;

export default function LoansRegister() {
  useDocumentTitle('Loans · Register');
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const allowed = hasPermission('loans:view');

  const initialFilters = useMemo(() => {
    const p = new URLSearchParams(window.location.search);
    return {
      status: p.get('status') ?? '',
      product: p.get('product') ?? '',
      dpd: p.get('dpd') ?? '',
    };
  }, []);

  const [status, setStatus] = useState(initialFilters.status);
  const [productID, setProductID] = useState(initialFilters.product);
  const [dpd, setDpd] = useState<string>(initialFilters.dpd);
  const [q, setQ] = useState('');
  const [products, setProducts] = useState<LoanProduct[]>([]);
  const [items, setItems] = useState<LoanListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!allowed) return;
    void listLoanProducts(true).then(setProducts).catch(() => {});
  }, [allowed]);

  const fetchList = useCallback(async () => {
    setBusy(true); setErr(null);
    try {
      const r = await listLoans({
        status: status || undefined,
        product_id: productID || undefined,
        q: q || undefined,
        limit: 200,
      });
      setItems(r.items);
      setTotal(r.total);
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }, [status, productID, q]);

  useEffect(() => {
    if (!allowed) return;
    void fetchList();
  }, [allowed, fetchList]);

  // DPD bucket — applied client-side over the fetched set. Phase 3's
  // backend filter replaces this; for now the in-memory filter is
  // cheap because the register is paginated to 200 rows.
  const filtered = useMemo(() => {
    if (!dpd) return items;
    return items.filter((it) => {
      const due = it.loan.first_due_date ? new Date(it.loan.first_due_date) : null;
      // Phase 1 heuristic — use first_due_date as a stand-in for
      // next_installment_due_at if the API doesn't expose the latter.
      const overdue = due ? due.getTime() < Date.now() : false;
      if (dpd === '1plus') return overdue;
      if (dpd === 'current') return !overdue;
      return true;
    });
  }, [items, dpd]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>Loans register</h1></div>
        <div className="alert alert-warn">You need <code>loans:view</code> permission.</div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Loans · Register</div>
          <h1>Loans</h1>
          <div className="page-sub">
            {filtered.length} of {total} loan{total === 1 ? '' : 's'} matching filter
          </div>
        </div>
        <button className="btn" disabled={busy} onClick={() => void fetchList()}>↻ Refresh</button>
      </div>

      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12 }}>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Status</div>
            <select className="input" value={status} onChange={(e) => setStatus(e.target.value)}>
              <option value="">All</option>
              {Object.entries(STATUS_LABEL).map(([k, l]) => (
                <option key={k} value={k}>{l}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Product</div>
            <select className="input" value={productID} onChange={(e) => setProductID(e.target.value)}>
              <option value="">All products</option>
              {products.map((p) => (
                <option key={p.id} value={p.id}>{p.code} · {p.name}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>DPD bucket</div>
            <select className="input" value={dpd} onChange={(e) => setDpd(e.target.value)}>
              {DPD_BUCKETS.map((b) => (
                <option key={b.id} value={b.id}>{b.label}</option>
              ))}
            </select>
          </label>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Search</div>
            <input
              className="input"
              type="text"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') void fetchList(); }}
              placeholder="loan_no or member name"
            />
          </label>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-body flush">
          {filtered.length === 0 && !busy ? (
            <div className="empty">No loans match the filter.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Loan no</th>
                  <th>Member</th>
                  <th>Product</th>
                  <th className="num">Principal</th>
                  <th className="num">Outstanding</th>
                  <th>Status</th>
                  <th>Next due</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((it) => {
                  const l = it.loan;
                  const outstanding =
                    (parseFloat(l.principal_balance) || 0) +
                    (parseFloat(l.interest_balance) || 0) +
                    (parseFloat(l.fees_balance) || 0) +
                    (parseFloat(l.penalty_balance) || 0);
                  return (
                    <tr
                      key={l.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => { window.location.href = `/loans/register/${l.id}`; }}
                      title="Open loan"
                    >
                      <td className="mono">{l.loan_no}</td>
                      <td>
                        <div style={{ fontWeight: 500 }}>{it.member_name}</div>
                        <div className="muted tiny mono">{it.member_no}</div>
                      </td>
                      <td>
                        <div>{it.product_name}</div>
                        <div className="muted tiny mono">{it.product_code}</div>
                      </td>
                      <td className="num mono">{currency} {fmt(l.principal)}</td>
                      <td className="num mono">{currency} {fmt(outstanding)}</td>
                      <td>{STATUS_LABEL[l.status] ?? l.status}</td>
                      <td className="tiny">{l.first_due_date ?? '—'}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function fmt(v: string | number | undefined): string {
  const n = typeof v === 'number' ? v : parseFloat(v ?? '0');
  if (!isFinite(n)) return String(v ?? '');
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
