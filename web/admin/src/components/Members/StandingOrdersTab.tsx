// DSID Phase 2.2 — member Standing Orders tab.
//
// Bordered, aligned table list with a status filter and per-row
// actions. Workflow-gated resume surfaces a notice when an approval
// was filed instead of a direct flip.

import { useEffect, useMemo, useState } from 'react';
import {
  StandingOrder, StandingOrderRun, MemberDepositItem,
  listMemberStandingOrders, createStandingOrder,
  cancelStandingOrder, patchStandingOrder, resumeStandingOrder,
  listStandingOrderRuns, getDepositAccountsByMember,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = { counterpartyId: string };

type SourceKind = 'manual_reminder' | 'payroll' | 'mpesa_pull' | 'fosa_debit';
type Frequency = 'weekly' | 'biweekly' | 'monthly' | 'quarterly';
type StatusFilter = 'all' | 'active' | 'paused' | 'suspended' | 'cancelled' | 'completed';

export default function StandingOrdersTab({ counterpartyId }: Props) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const [rows, setRows] = useState<StandingOrder[]>([]);
  const [accounts, setAccounts] = useState<MemberDepositItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [runsFor, setRunsFor] = useState<StandingOrder | null>(null);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');

  async function refresh() {
    setLoading(true);
    setErr(null);
    try {
      const [items, accts] = await Promise.all([
        listMemberStandingOrders(counterpartyId),
        getDepositAccountsByMember(counterpartyId).catch(() => [] as MemberDepositItem[]),
      ]);
      setRows(items);
      setAccounts(accts);
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Failed to load.');
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => { void refresh(); }, [counterpartyId]);

  const accountByID = useMemo(() => {
    const m = new Map<string, MemberDepositItem>();
    for (const a of accounts) m.set(a.account.id, a);
    return m;
  }, [accounts]);

  const counts = useMemo(() => {
    const c: Record<string, number> = { all: rows.length };
    for (const r of rows) c[r.status] = (c[r.status] ?? 0) + 1;
    return c;
  }, [rows]);

  const filtered = useMemo(() => {
    if (statusFilter === 'all') return rows;
    return rows.filter(r => r.status === statusFilter);
  }, [rows, statusFilter]);

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: 12 }}>
        <div>
          <h3 style={{ margin: 0 }}>Standing orders</h3>
          <div className="muted tiny" style={{ marginTop: 2 }}>
            Recurring contributions — payroll, M-PESA, FOSA debit, or SMS reminders.
          </div>
        </div>
        <button className="btn btn-sm btn-primary" onClick={() => setShowCreate(true)}>
          + New standing order
        </button>
      </div>

      {/* Status filter */}
      <div className="row" style={{ gap: 4, marginBottom: 10, flexWrap: 'wrap' }}>
        {(['all', 'active', 'paused', 'suspended', 'cancelled', 'completed'] as StatusFilter[]).map(s => (
          <button
            key={s}
            className="btn btn-sm"
            onClick={() => setStatusFilter(s)}
            style={{
              borderColor: statusFilter === s ? 'var(--accent)' : 'var(--border)',
              background: statusFilter === s ? 'var(--accent-bg)' : 'transparent',
              color: statusFilter === s ? 'var(--accent-fg)' : 'var(--fg-2)',
            }}
          >
            {s === 'all' ? 'All' : s.charAt(0).toUpperCase() + s.slice(1)}
            {(counts[s] ?? 0) > 0 && <span style={{ marginLeft: 6, opacity: 0.7 }}>{counts[s]}</span>}
          </button>
        ))}
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {loading ? (
        <div className="muted">Loading…</div>
      ) : filtered.length === 0 ? (
        <EmptyState
          totalRows={rows.length}
          filter={statusFilter}
          onCreate={() => setShowCreate(true)}
          onClearFilter={() => setStatusFilter('all')}
        />
      ) : (
        <div
          className="card"
          style={{ padding: 0, overflow: 'hidden' }}
        >
          <table className="tbl">
            <thead>
              <tr>
                <th>Status</th>
                <th>Source</th>
                <th>From → To</th>
                <th>Frequency</th>
                <th className="num">Amount ({currency})</th>
                <th>Next run</th>
                <th className="num">Fails</th>
                <th style={{ textAlign: 'right', paddingRight: 12 }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(r => (
                <StandingOrderRow
                  key={r.id}
                  so={r}
                  accountByID={accountByID}
                  onAction={refresh}
                  onViewRuns={() => setRunsFor(r)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && (
        <CreateForm
          counterpartyId={counterpartyId}
          accounts={accounts}
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); void refresh(); }}
        />
      )}
      {runsFor && (
        <RunsModal so={runsFor} currency={currency} onClose={() => setRunsFor(null)} />
      )}
    </div>
  );
}

// ─────────── Row ───────────

function StandingOrderRow({ so, accountByID, onAction, onViewRuns }: {
  so: StandingOrder;
  accountByID: Map<string, MemberDepositItem>;
  onAction: () => void;
  onViewRuns: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const target = accountByID.get(so.target_account_id);
  const sourceAccount = so.source_account_id ? accountByID.get(so.source_account_id) : undefined;
  const isTerminal = so.status === 'cancelled' || so.status === 'completed';

  async function flip(status: 'active' | 'paused') {
    setBusy(true);
    try { await patchStandingOrder(so.id, { status }); onAction(); } finally { setBusy(false); }
  }
  async function cancel() {
    if (!confirm('Cancel this standing order? This is a soft-delete.')) return;
    setBusy(true);
    try { await cancelStandingOrder(so.id); onAction(); } finally { setBusy(false); }
  }
  async function resume() {
    setBusy(true);
    try {
      const res = await resumeStandingOrder(so.id);
      if (res.status === 'approval_required') {
        alert('A workflow approval has been filed (recent suspend < 7 days).');
      }
      onAction();
    } catch (e: any) {
      alert(e?.response?.data?.error?.message || e?.message || 'Resume failed.');
    } finally { setBusy(false); }
  }

  const targetLabel = target ? `${target.account.account_no}` : so.target_account_id.slice(0, 8) + '…';
  const sourceLabel = sourceLabelFor(so, sourceAccount);

  return (
    <tr style={{ opacity: isTerminal ? 0.6 : 1 }}>
      <td><StatusBadge status={so.status} /></td>
      <td><SourceChip source={so.source} /></td>
      <td>
        <span className="muted">{sourceLabel}</span>
        <span style={{ color: 'var(--fg-4)', margin: '0 6px' }}>→</span>
        <span style={{ fontWeight: 500 }}>{targetLabel}</span>
      </td>
      <td>{labelForFrequency(so.frequency)}</td>
      <td className="num">{formatAmount(so.amount)}</td>
      <td>
        {so.status === 'active' || so.status === 'paused' ? (
          <>
            <div>{fmtDate(so.next_run_at)}</div>
            <div className="muted tiny">{relativeDate(so.next_run_at)}</div>
          </>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
      <td className="num">
        <span style={{ color: so.consecutive_failures > 0 ? 'var(--neg)' : 'var(--fg-3)' }}>
          {so.consecutive_failures}
        </span>
      </td>
      <td style={{ textAlign: 'right', paddingRight: 12 }}>
        <div className="row" style={{ gap: 4, justifyContent: 'flex-end' }}>
          <button className="btn btn-sm" onClick={onViewRuns}>Runs</button>
          {so.status === 'active' && (
            <button className="btn btn-sm" disabled={busy} onClick={() => flip('paused')}>Pause</button>
          )}
          {so.status === 'paused' && (
            <button className="btn btn-sm" disabled={busy} onClick={() => flip('active')}>Resume</button>
          )}
          {so.status === 'suspended' && (
            <button className="btn btn-sm" disabled={busy} onClick={resume}>Resume</button>
          )}
          {(so.status === 'active' || so.status === 'paused') && (
            <button className="btn btn-sm btn-danger" disabled={busy} onClick={cancel}>Cancel</button>
          )}
        </div>
      </td>
    </tr>
  );
}

function sourceLabelFor(so: StandingOrder, sourceAccount?: MemberDepositItem): string {
  switch (so.source) {
    case 'fosa_debit':
      return sourceAccount?.account.account_no
        ?? (so.source_account_id ? so.source_account_id.slice(0, 8) + '…' : 'FOSA');
    case 'mpesa_pull':
      return so.source_msisdn ?? 'MSISDN';
    case 'payroll':
      return so.source_payroll_employer ?? 'Payroll';
    case 'manual_reminder':
      return 'SMS reminder';
  }
}

// ─────────── Empty state ───────────

function EmptyState({ totalRows, filter, onCreate, onClearFilter }: {
  totalRows: number;
  filter: StatusFilter;
  onCreate: () => void;
  onClearFilter: () => void;
}) {
  if (totalRows === 0) {
    return (
      <div
        style={{
          padding: '32px 16px',
          textAlign: 'center',
          border: '1px dashed var(--border)',
          borderRadius: 'var(--r-md)',
          background: 'var(--surface-2)',
        }}
      >
        <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 4 }}>No standing orders yet.</div>
        <div className="muted tiny" style={{ marginBottom: 12 }}>
          Set up a recurring contribution and the processor takes care of the rest.
        </div>
        <button className="btn btn-sm btn-primary" onClick={onCreate}>+ Create first standing order</button>
      </div>
    );
  }
  return (
    <div className="muted" style={{ padding: '24px 0', textAlign: 'center' }}>
      No {filter} standing orders. <button className="btn btn-sm btn-ghost" onClick={onClearFilter}>Clear filter</button>
    </div>
  );
}

// ─────────── Badges ───────────

function StatusBadge({ status }: { status: string }) {
  const cls = ({
    active:    'badge badge-pos',
    paused:    'badge badge-warn',
    suspended: 'badge badge-neg',
    cancelled: 'badge badge-outline',
    completed: 'badge badge-info',
  } as Record<string, string>)[status] || 'badge';
  return <span className={cls}>{status}</span>;
}

function SourceChip({ source }: { source: string }) {
  const label = ({
    fosa_debit:      'FOSA debit',
    mpesa_pull:      'M-PESA pull',
    payroll:         'Payroll',
    manual_reminder: 'SMS reminder',
  } as Record<string, string>)[source] || source;
  return <span className="badge badge-outline">{label}</span>;
}

// ─────────── Create form ───────────

function CreateForm({ counterpartyId, accounts, onClose, onCreated }: {
  counterpartyId: string;
  accounts: MemberDepositItem[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const [source, setSource] = useState<SourceKind>('fosa_debit');
  const [targetAccountID, setTargetAccountID] = useState('');
  const [sourceAccountID, setSourceAccountID] = useState('');
  const [sourceMSISDN, setSourceMSISDN] = useState('');
  const [sourceEmployer, setSourceEmployer] = useState('');
  const [amount, setAmount] = useState('');
  const [frequency, setFrequency] = useState<Frequency>('monthly');
  const [startDate, setStartDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [endDate, setEndDate] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (accounts.length > 0 && !targetAccountID) {
      setTargetAccountID(accounts[0].account.id);
    }
  }, [accounts, targetAccountID]);

  const amountNum = useMemo(() => {
    const n = Number(amount);
    return Number.isFinite(n) && n > 0 ? n : null;
  }, [amount]);

  const cadence = useMemo(() => describeCadence(frequency, startDate, endDate, amountNum, currency), [frequency, startDate, endDate, amountNum, currency]);

  const sourceReqMissing =
    (source === 'fosa_debit' && !sourceAccountID) ||
    (source === 'mpesa_pull' && !sourceMSISDN) ||
    (source === 'payroll' && !sourceEmployer);
  const canSubmit = !!targetAccountID && !!amountNum && !sourceReqMissing && !busy;

  async function submit() {
    setErr(null); setBusy(true);
    try {
      await createStandingOrder(counterpartyId, {
        target_account_id: targetAccountID,
        source,
        source_account_id: source === 'fosa_debit' ? sourceAccountID : undefined,
        source_msisdn: source === 'mpesa_pull' ? sourceMSISDN : undefined,
        source_payroll_employer: source === 'payroll' ? sourceEmployer : undefined,
        amount,
        frequency,
        start_date: startDate,
        end_date: endDate || undefined,
      } as any);
      onCreated();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Create failed.');
    } finally { setBusy(false); }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 560 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-section">
          <h3 style={{ marginTop: 0, marginBottom: 2 }}>New standing order</h3>
          <div className="muted tiny">
            A recurring contribution that the standing-order processor runs on schedule.
          </div>
        </div>

        <div className="modal-section">
          <div className="eyebrow" style={{ marginBottom: 6 }}>Source</div>
          <div className="radio-cards">
            <SourceCard k="fosa_debit" cur={source} set={setSource}
              title="FOSA debit"
              sub="Auto-debit from the member's FOSA account on each due date." />
            <SourceCard k="mpesa_pull" cur={source} set={setSource}
              title="M-PESA pull"
              sub="System fires STK push to the member's phone on each due date." />
            <SourceCard k="payroll" cur={source} set={setSource}
              title="Payroll check-off"
              sub="Pulled from the employer's monthly salary CSV." />
            <SourceCard k="manual_reminder" cur={source} set={setSource}
              title="SMS reminder"
              sub="System SMSes the member; member pays manually." />
          </div>
        </div>

        {source === 'fosa_debit' && (
          <div className="modal-section">
            <AccountPicker
              label="Debit FROM (FOSA account)"
              required
              accounts={accounts}
              value={sourceAccountID}
              onChange={setSourceAccountID}
              hint="The source account funds are pulled from."
              excludeID={targetAccountID}
            />
          </div>
        )}
        {source === 'mpesa_pull' && (
          <div className="modal-section">
            <div className="field">
              <label className="field-label">Source MSISDN <span className="req">*</span></label>
              <input className="input" inputMode="tel" value={sourceMSISDN}
                onChange={(e) => setSourceMSISDN(e.target.value)}
                placeholder="2547XXXXXXXX" />
              <div className="field-hint">The phone the STK Push prompt goes to. Use the Daraja format (no leading +).</div>
            </div>
          </div>
        )}
        {source === 'payroll' && (
          <div className="modal-section">
            <div className="field">
              <label className="field-label">Employer code <span className="req">*</span></label>
              <input className="input" value={sourceEmployer}
                onChange={(e) => setSourceEmployer(e.target.value.toUpperCase())}
                placeholder="ACME" />
              <div className="field-hint">Must match <code>checkoff_batches.employer_code</code> uploaded each month.</div>
            </div>
          </div>
        )}

        <div className="modal-section">
          <AccountPicker
            label="Credit INTO (target account)"
            required
            accounts={accounts}
            value={targetAccountID}
            onChange={setTargetAccountID}
            hint="The account that receives each contribution."
            excludeID={source === 'fosa_debit' ? sourceAccountID : undefined}
          />
        </div>

        <div className="modal-section grid-2">
          <div className="field">
            <label className="field-label">Amount <span className="req">*</span></label>
            <div style={{ position: 'relative' }}>
              <span style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', fontSize: 11, color: 'var(--fg-3)' }}>{currency}</span>
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
            <label className="field-label">Frequency <span className="req">*</span></label>
            <select className="select" value={frequency} onChange={(e) => setFrequency(e.target.value as Frequency)}>
              <option value="weekly">Weekly</option>
              <option value="biweekly">Biweekly (every 2 weeks)</option>
              <option value="monthly">Monthly</option>
              <option value="quarterly">Quarterly</option>
            </select>
          </div>
        </div>

        <div className="modal-section grid-2">
          <div className="field">
            <label className="field-label">Start date <span className="req">*</span></label>
            <input className="input" type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} />
            <div className="field-hint">First run lands at 06:00 UTC on this date.</div>
          </div>
          <div className="field">
            <label className="field-label">End date</label>
            <input className="input" type="date" value={endDate} onChange={(e) => setEndDate(e.target.value)} />
            <div className="field-hint">Optional — leave empty for an open-ended order.</div>
          </div>
        </div>

        <div className="modal-section">
          <div className="cadence-summary">{cadence}</div>
        </div>

        {err && <div className="alert alert-error">{err}</div>}

        <div className="modal-actions">
          <button className="btn btn-sm" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn btn-sm btn-primary" disabled={!canSubmit} onClick={() => void submit()}>
            {busy ? 'Creating…' : 'Create standing order'}
          </button>
        </div>
      </div>
    </div>
  );
}

function SourceCard({ k, cur, set, title, sub }: {
  k: SourceKind; cur: SourceKind; set: (k: SourceKind) => void;
  title: string; sub: string;
}) {
  return (
    <div className="radio-card" data-selected={cur === k} onClick={() => set(k)} role="button" tabIndex={0}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') set(k); }}>
      <div className="rc-title">{title}</div>
      <div className="rc-sub">{sub}</div>
    </div>
  );
}

function AccountPicker({ label, required, accounts, value, onChange, hint, excludeID }: {
  label: string;
  required?: boolean;
  accounts: MemberDepositItem[];
  value: string;
  onChange: (v: string) => void;
  hint?: string;
  excludeID?: string;
}) {
  const filtered = excludeID ? accounts.filter(a => a.account.id !== excludeID) : accounts;
  return (
    <div className="field">
      <label className="field-label">{label}{required && <span className="req">*</span>}</label>
      {filtered.length === 0 ? (
        <div className="muted tiny">No eligible accounts for this member.</div>
      ) : (
        <select className="select" value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">— select an account —</option>
          {filtered.map(({ account, product }) => (
            <option key={account.id} value={account.id}>
              {account.account_no} · {product.name} · balance {formatAmount(account.current_balance)}
            </option>
          ))}
        </select>
      )}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

// ─────────── Helpers ───────────

function describeCadence(frequency: Frequency, startDate: string, endDate: string, amount: number | null, currency: string): string | JSX.Element {
  if (!startDate) return 'Set a start date to preview cadence.';
  const start = new Date(startDate + 'T06:00:00Z');
  if (isNaN(start.getTime())) return 'Invalid start date.';
  const cadenceLabel = ({
    weekly:    'every week',
    biweekly:  'every 2 weeks',
    monthly:   'every month',
    quarterly: 'every 3 months',
  } as Record<Frequency, string>)[frequency];
  const dateFmt = (d: Date) => d.toLocaleDateString(undefined, { day: '2-digit', month: 'short', year: 'numeric' });
  const amountStr = amount ? `${currency} ${amount.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}` : `${currency} —`;
  const tail = endDate ? `until ${dateFmt(new Date(endDate))}` : 'with no end date';
  return (
    <span>
      <strong>{amountStr}</strong> {cadenceLabel}, starting <strong>{dateFmt(start)}</strong> {tail}.
    </span>
  ) as any;
}

function formatAmount(s: string | number): string {
  const n = typeof s === 'number' ? s : Number(s);
  if (!Number.isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function labelForFrequency(f: string): string {
  return ({
    weekly: 'Weekly',
    biweekly: 'Biweekly',
    monthly: 'Monthly',
    quarterly: 'Quarterly',
  } as Record<string, string>)[f] || f;
}

function fmtDate(s?: string): string {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  return d.toLocaleDateString(undefined, { day: '2-digit', month: 'short', year: 'numeric' });
}

// relativeDate — "in 3 days" / "2 hours ago" / falls back to date.
function relativeDate(s?: string): string {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  const diffMs = d.getTime() - Date.now();
  const abs = Math.abs(diffMs);
  const future = diffMs >= 0;
  const min = 60_000;
  const hr = 60 * min;
  const day = 24 * hr;
  let val: string;
  if (abs < hr) val = `${Math.max(1, Math.round(abs / min))} min`;
  else if (abs < day) val = `${Math.round(abs / hr)} h`;
  else if (abs < 60 * day) val = `${Math.round(abs / day)} d`;
  else return d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' });
  return future ? `in ${val}` : `${val} ago`;
}

// ─────────── Runs modal ───────────

function RunsModal({ so, currency, onClose }: { so: StandingOrder; currency: string; onClose: () => void }) {
  const [runs, setRuns] = useState<StandingOrderRun[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    listStandingOrderRuns(so.id).then(r => { setRuns(r); setLoading(false); });
  }, [so.id]);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" style={{ maxWidth: 720 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-section">
          <h3 style={{ marginTop: 0, marginBottom: 2 }}>Run history</h3>
          <div className="muted tiny">
            <SourceChip source={so.source} /> · {currency} {formatAmount(so.amount)} · {labelForFrequency(so.frequency)}
          </div>
        </div>
        {loading ? (
          <div className="muted">Loading…</div>
        ) : runs.length === 0 ? (
          <div className="muted" style={{ padding: '16px 0', textAlign: 'center' }}>No runs yet.</div>
        ) : (
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            <table className="tbl">
              <thead>
                <tr>
                  <th>When</th>
                  <th>Period</th>
                  <th className="num">Attempt</th>
                  <th>Status</th>
                  <th>Error</th>
                </tr>
              </thead>
              <tbody>
                {runs.map(r => (
                  <tr key={r.id}>
                    <td className="tiny">{new Date(r.attempted_at).toLocaleString()}</td>
                    <td>{r.period_label}</td>
                    <td className="num">{r.attempt_no}</td>
                    <td><RunStatus status={r.status} /></td>
                    <td className="muted tiny">{[r.error_code, r.error_message].filter(Boolean).join(' — ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="modal-actions">
          <button className="btn btn-sm" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  );
}

function RunStatus({ status }: { status: string }) {
  const cls = ({
    success: 'badge badge-pos',
    failed:  'badge badge-neg',
    partial: 'badge badge-warn',
    skipped: 'badge badge-outline',
  } as Record<string, string>)[status] || 'badge';
  return <span className={cls}>{status}</span>;
}
