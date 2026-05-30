// DSID Phase 2.2 — per-product Recurring Fees editor.
//
// Drop-in section for /deposit-products/{id}/edit. CRUD on
// deposit_product_recurring_fees. Each row: kind, amount, frequency,
// GL code, effective dates, toggle active.

import { useEffect, useMemo, useState } from 'react';
import {
  RecurringFee,
  listProductRecurringFees, createProductRecurringFee,
  patchRecurringFee, deleteRecurringFee,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = { productID: string };

type Frequency = 'monthly' | 'quarterly' | 'annual';

const FEE_KIND_SUGGESTIONS = [
  { value: 'maintenance', label: 'Maintenance' },
  { value: 'statement', label: 'Statement' },
  { value: 'mpesa_pull', label: 'M-PESA pull' },
  { value: 'sms_alerts', label: 'SMS alerts' },
  { value: 'dormancy', label: 'Dormancy' },
];

export default function RecurringFeesEditor({ productID }: Props) {
  const [rows, setRows] = useState<RecurringFee[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  async function refresh() {
    setLoading(true); setErr(null);
    try {
      setRows(await listProductRecurringFees(productID));
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.');
    } finally { setLoading(false); }
  }
  useEffect(() => { void refresh(); }, [productID]);

  return (
    <div className="card" style={{ padding: 14, marginTop: 16 }}>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 12 }}>
        <div>
          <h3 style={{ margin: 0 }}>Recurring fees</h3>
          <div className="muted tiny" style={{ marginTop: 2 }}>
            Fees the recurring-fee charger posts on schedule against every active account on this product.
          </div>
        </div>
        <button className="btn btn-sm btn-primary" onClick={() => setShowCreate(true)}>+ Add fee</button>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {loading ? (
        <div className="muted">Loading…</div>
      ) : rows.length === 0 ? (
        <div className="muted" style={{ padding: '20px 0', textAlign: 'center' }}>
          No recurring fees configured for this product.
        </div>
      ) : (
        <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
          <table className="tbl">
            <thead>
              <tr>
                <th>Kind</th>
                <th className="num">Amount</th>
                <th>Frequency</th>
                <th>GL credit</th>
                <th>Effective</th>
                <th>Status</th>
                <th style={{ textAlign: 'right', paddingRight: 12 }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(r => <Row key={r.id} fee={r} onChange={refresh} />)}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && (
        <CreateForm
          productID={productID}
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); void refresh(); }}
        />
      )}
    </div>
  );
}

function Row({ fee, onChange }: { fee: RecurringFee; onChange: () => void }) {
  const [busy, setBusy] = useState(false);
  async function toggle() {
    setBusy(true);
    try { await patchRecurringFee(fee.id, { active: !fee.active }); onChange(); } finally { setBusy(false); }
  }
  async function drop() {
    if (!confirm('Delete this fee? Existing charge rows are preserved.')) return;
    setBusy(true);
    try { await deleteRecurringFee(fee.id); onChange(); } finally { setBusy(false); }
  }
  return (
    <tr>
      <td>{labelForFeeKind(fee.fee_kind)}</td>
      <td className="num" style={{ fontFamily: 'var(--font-mono)' }}>{formatAmount(fee.amount)}</td>
      <td>{labelForFrequency(fee.frequency)}</td>
      <td><code>{fee.gl_credit_code}</code></td>
      <td className="muted tiny">
        {fee.starts_on.slice(0, 10)} → {fee.ends_on?.slice(0, 10) ?? '∞'}
      </td>
      <td>
        <span className={fee.active ? 'badge badge-pos' : 'badge badge-outline'}>
          {fee.active ? 'Active' : 'Disabled'}
        </span>
      </td>
      <td>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          <button className="btn btn-sm" disabled={busy} onClick={() => void toggle()}>
            {fee.active ? 'Disable' : 'Enable'}
          </button>
          <button className="btn btn-sm btn-danger" disabled={busy} onClick={() => void drop()}>
            Delete
          </button>
        </div>
      </td>
    </tr>
  );
}

// ─────────── Create form ───────────

function CreateForm({ productID, onClose, onCreated }: {
  productID: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const [feeKind, setFeeKind] = useState('maintenance');
  const [amount, setAmount] = useState('');
  const [frequency, setFrequency] = useState<Frequency>('monthly');
  const [gl, setGL] = useState('4120');
  const [startsOn, setStartsOn] = useState(() => new Date().toISOString().slice(0, 10));
  const [endsOn, setEndsOn] = useState('');
  const [notes, setNotes] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const amountNum = useMemo(() => {
    const n = Number(amount);
    return Number.isFinite(n) && n > 0 ? n : null;
  }, [amount]);

  const canSubmit = !!feeKind && !!gl && !!amountNum && !busy;

  const cadenceLine = useMemo(() => {
    if (!amountNum) return 'Set an amount to preview the charge.';
    const freqLabel = { monthly: 'every month', quarterly: 'every quarter', annual: 'every year' }[frequency];
    return `${currency} ${amountNum.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${freqLabel}, posted on the first day after the period starts.`;
  }, [amountNum, frequency, currency]);

  async function submit() {
    setErr(null); setBusy(true);
    try {
      await createProductRecurringFee(productID, {
        fee_kind: feeKind, amount, frequency, gl_credit_code: gl,
        starts_on: startsOn, ends_on: endsOn || undefined, notes,
      } as any);
      onCreated();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Create failed.');
    } finally { setBusy(false); }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 520 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-section">
          <h3 style={{ marginTop: 0, marginBottom: 2 }}>New recurring fee</h3>
          <div className="muted tiny">Posts on every active account on this product, once per period.</div>
        </div>

        <div className="modal-section grid-2">
          <div className="field">
            <label className="field-label">Fee kind <span className="req">*</span></label>
            <input
              className="input"
              list="fee-kind-suggestions"
              value={feeKind}
              onChange={(e) => setFeeKind(e.target.value)}
              placeholder="e.g. maintenance"
            />
            <datalist id="fee-kind-suggestions">
              {FEE_KIND_SUGGESTIONS.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}
            </datalist>
            <div className="field-hint">Free-form slug; used in member statements and audit rows.</div>
          </div>
          <div className="field">
            <label className="field-label">GL credit code <span className="req">*</span></label>
            <input
              className="input"
              style={{ fontFamily: 'var(--font-mono)' }}
              value={gl}
              onChange={(e) => setGL(e.target.value.trim())}
              placeholder="4120"
            />
            <div className="field-hint">Mirrors <code>fee_catalog.gl_credit_code</code>.</div>
          </div>
        </div>

        <div className="modal-section">
          <div className="eyebrow" style={{ marginBottom: 6 }}>Frequency</div>
          <div className="radio-cards" style={{ gridTemplateColumns: 'repeat(3, 1fr)' }}>
            <FreqCard k="monthly" cur={frequency} set={setFrequency} title="Monthly" sub="Period: YYYY-MM" />
            <FreqCard k="quarterly" cur={frequency} set={setFrequency} title="Quarterly" sub="Period: YYYY-QN" />
            <FreqCard k="annual" cur={frequency} set={setFrequency} title="Annual" sub="Period: YYYY" />
          </div>
        </div>

        <div className="modal-section grid-2">
          <div className="field">
            <label className="field-label">Amount <span className="req">*</span></label>
            <div style={{ position: 'relative' }}>
              <span
                style={{
                  position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)',
                  fontSize: 11, color: 'var(--fg-3)',
                }}
              >{currency}</span>
              <input
                className="input"
                style={{ paddingLeft: 38, fontFamily: 'var(--font-mono)' }}
                inputMode="decimal"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                placeholder="0.00"
              />
            </div>
          </div>
          <div className="field">
            <label className="field-label">Notes</label>
            <input
              className="input"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Optional"
            />
          </div>
        </div>

        <div className="modal-section grid-2">
          <div className="field">
            <label className="field-label">Starts on <span className="req">*</span></label>
            <input className="input" type="date" value={startsOn} onChange={(e) => setStartsOn(e.target.value)} />
            <div className="field-hint">First period billed on/after this date.</div>
          </div>
          <div className="field">
            <label className="field-label">Ends on</label>
            <input className="input" type="date" value={endsOn} onChange={(e) => setEndsOn(e.target.value)} />
            <div className="field-hint">Leave empty for no end date.</div>
          </div>
        </div>

        <div className="modal-section">
          <div className="cadence-summary">{cadenceLine}</div>
        </div>

        {err && <div className="alert alert-error">{err}</div>}

        <div className="modal-actions">
          <button className="btn btn-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn btn-sm btn-primary" disabled={!canSubmit} onClick={() => void submit()}>
            {busy ? 'Creating…' : 'Create fee'}
          </button>
        </div>
      </div>
    </div>
  );
}

function FreqCard({ k, cur, set, title, sub }: {
  k: Frequency; cur: Frequency; set: (k: Frequency) => void;
  title: string; sub: string;
}) {
  return (
    <div
      className="radio-card"
      data-selected={cur === k}
      onClick={() => set(k)}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') set(k); }}
    >
      <div className="rc-title">{title}</div>
      <div className="rc-sub">{sub}</div>
    </div>
  );
}

// ─────────── Helpers ───────────

function labelForFrequency(f: string): string {
  return ({ monthly: 'Monthly', quarterly: 'Quarterly', annual: 'Annual' } as Record<string, string>)[f] || f;
}
function labelForFeeKind(k: string): string {
  const hit = FEE_KIND_SUGGESTIONS.find(s => s.value === k);
  return hit ? hit.label : k;
}
function formatAmount(s: string | number): string {
  const n = typeof s === 'number' ? s : Number(s);
  if (!Number.isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
