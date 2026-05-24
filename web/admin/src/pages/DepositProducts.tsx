// Deposit product configuration — list + create + edit form.
//
// Two visible shapes:
//   • Flag off (legacy): one flat product list, the seven FOSA-style
//     types only, form has no segment selector.
//   • Flag on (BOSA_FOSA): segment column + filter chips, an eighth
//     product type (member_deposit), a Segment radio at the top of
//     the modal that gates the rest of the form.
//
// The behind-the-scenes API and column shape are the same in both
// modes — the flag only controls what's surfaced.

import { useEffect, useState } from 'react';
import {
  createDepositProduct,
  deleteDepositProduct,
  extractError,
  getInboxStatus,
  listDepositProducts,
  updateDepositProduct,
  type DepositEligibility,
  type DepositProduct,
  type DepositProductType,
  type DepositSegment,
  type FeeFrequency,
  type MaturityAction,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';

const PRODUCT_LABEL: Record<DepositProductType, string> = {
  ordinary: 'Ordinary',
  fixed: 'Fixed deposit',
  junior: 'Junior',
  holiday: 'Holiday',
  goal: 'Goal',
  emergency: 'Emergency',
  group: 'Group',
  member_deposit: 'Member deposit',
};

// FOSA = withdrawable savings (the seven legacy types). BOSA =
// member-deposit bond (just `member_deposit` for now; tenants may
// add more BOSA-coded products later).
const FOSA_TYPES: DepositProductType[] = ['ordinary', 'fixed', 'junior', 'holiday', 'goal', 'emergency', 'group'];
const BOSA_TYPES: DepositProductType[] = ['member_deposit'];

export default function DepositProducts() {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canEdit = hasPermission('deposits:configure');

  const [products, setProducts] = useState<DepositProduct[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<DepositProduct | 'new' | null>(null);
  // BOSA_FOSA gating. null while loading (legacy shape paints in
  // the meantime); once known it never flips during the session.
  const [segmentEnabled, setSegmentEnabled] = useState<boolean | null>(null);
  // 'all' | 'bosa' | 'fosa'. Only meaningful when segmentEnabled.
  const [segmentFilter, setSegmentFilter] = useState<'all' | DepositSegment>('all');

  async function reload() {
    setErr(null);
    try { setProducts(await listDepositProducts({ includeInactive: true })); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); }, []);
  useEffect(() => {
    getInboxStatus()
      .then((s) => setSegmentEnabled(s.bosa_fosa_enabled))
      .catch(() => setSegmentEnabled(false)); // flag endpoint down → safe default
  }, []);

  // Display list — segment-filtered when the flag is on AND a chip
  // other than "All" is active. Otherwise pass-through.
  const display = (products ?? []).filter((p) =>
    !segmentEnabled || segmentFilter === 'all' ? true : p.segment === segmentFilter
  );

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Deposits · Configuration</div>
          <h1>Deposit products</h1>
          <div className="page-sub">Define the savings products members can hold.</div>
        </div>
        <div className="page-hd-actions">
          {canEdit && (
            <button className="btn btn-sm btn-accent" onClick={() => setEditing('new')}>
              <Icon name="plus" size={12} /> New product
            </button>
          )}
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <div className="card">
        <div className="card-hd">
          <h3>Catalog</h3>
          <span className="card-sub">
            {segmentEnabled && segmentFilter !== 'all'
              ? `${display.length} of ${products?.length ?? 0} products`
              : `${products?.length ?? 0} products`}
          </span>
        </div>

        {segmentEnabled && (
          <div className="card-body" style={{ display: 'flex', gap: 6, paddingBottom: 8 }}>
            {(['all', 'bosa', 'fosa'] as const).map((s) => (
              <button
                key={s}
                className={`btn btn-sm${segmentFilter === s ? ' btn-accent' : ''}`}
                onClick={() => setSegmentFilter(s)}
              >
                {s === 'all' ? 'All' : s === 'bosa' ? 'BOSA · Member deposits' : 'FOSA · Withdrawable savings'}
              </button>
            ))}
          </div>
        )}

        <div className="card-body flush">
          {!products && <div className="empty">Loading…</div>}
          {products && display.length === 0 && (
            <div className="empty">
              {segmentEnabled && segmentFilter !== 'all'
                ? `No ${segmentFilter.toUpperCase()} products configured yet.`
                : 'No products yet.'}
              {canEdit && products.length === 0 && (
                <> {' '}
                  <a style={{ color: 'var(--accent)' }} onClick={() => setEditing('new')}>Create the first one →</a>
                </>
              )}
            </div>
          )}
          {products && display.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Name</th>
                  {segmentEnabled && <th>Segment</th>}
                  <th>Type</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Min open</th>
                  <th style={{ textAlign: 'right' }}>Min op bal</th>
                  <th>Constraints</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {display.map((p) => (
                  <tr key={p.id}>
                    <td className="tiny-mono">{p.code}</td>
                    <td>{p.name}</td>
                    {segmentEnabled && (
                      <td>
                        {p.segment === 'bosa'
                          ? <Badge tone="warn">BOSA</Badge>
                          : <Badge tone="neutral">FOSA</Badge>}
                      </td>
                    )}
                    <td><Badge tone="accent">{PRODUCT_LABEL[p.product_type]}</Badge></td>
                    <td>{p.is_active ? <Badge tone="pos">active</Badge> : <Badge tone="neutral">archived</Badge>}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {p.min_opening_balance}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {p.min_operating_balance}</td>
                    <td className="tiny">
                      {/* BOSA: surface the recurring-contribution schedule.
                          FOSA: the existing withdrawal-rule chips. */}
                      {p.segment === 'bosa' && Number(p.required_monthly_amount) > 0 && (
                        <span>{currency} {p.required_monthly_amount}/mo · day {p.required_day_of_month ?? '—'}</span>
                      )}
                      {p.segment !== 'bosa' && (
                        <>
                          {p.lock_in_months > 0 && <span>Lock-in {p.lock_in_months}m · </span>}
                          {p.notice_period_days > 0 && <span>Notice {p.notice_period_days}d · </span>}
                          {p.max_withdrawals_per_month != null && <span>{p.max_withdrawals_per_month}/mo · </span>}
                          {p.withdrawal_window_start_month && p.withdrawal_window_end_month &&
                            <span>Window {p.withdrawal_window_start_month}–{p.withdrawal_window_end_month} · </span>}
                          {p.large_withdrawal_threshold && <span>Large &gt; {currency} {p.large_withdrawal_threshold}</span>}
                        </>
                      )}
                    </td>
                    <td>
                      {canEdit && (
                        <button className="btn btn-sm" onClick={() => setEditing(p)}>Edit</button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {editing && canEdit && (
        <ProductModal
          initial={editing === 'new' ? null : editing}
          currency={currency}
          segmentEnabled={!!segmentEnabled}
          onClose={() => setEditing(null)}
          onSaved={async () => { setEditing(null); await reload(); }}
        />
      )}
    </div>
  );
}

// ─────────── Modal: create / edit ───────────

function ProductModal({ initial, currency, segmentEnabled, onClose, onSaved }: {
  initial: DepositProduct | null;
  currency: string;
  segmentEnabled: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isNew = initial === null;
  const [code, setCode] = useState(initial?.code ?? '');
  const [name, setName] = useState(initial?.name ?? '');
  // Segment is shown only when the flag is on. When the flag is OFF
  // we leave it at FOSA so the create endpoint's default-inference
  // still routes to the right bucket.
  const [segment, setSegment] = useState<DepositSegment>(initial?.segment ?? 'fosa');
  // Pre-select a sensible default product_type for whichever segment
  // the user starts with. Switching segment in the radio updates this
  // so the dropdown never shows an out-of-bucket value.
  const [productType, setProductType] = useState<DepositProductType>(
    initial?.product_type ?? (segment === 'bosa' ? 'member_deposit' : 'ordinary')
  );
  const [desc, setDesc] = useState(initial?.description ?? '');
  const [isActive, setIsActive] = useState(initial?.is_active ?? true);

  // BOSA-only fields.
  const [reqMonthly, setReqMonthly] = useState(initial?.required_monthly_amount ?? '0');
  const [reqDay, setReqDay] = useState<string>(initial?.required_day_of_month != null ? String(initial.required_day_of_month) : '');
  const isBOSA = segment === 'bosa';

  const [minOpen, setMinOpen] = useState(initial?.min_opening_balance ?? '0');
  const [minOp, setMinOp] = useState(initial?.min_operating_balance ?? '0');
  const [maxBal, setMaxBal] = useState(initial?.max_balance ?? '');
  const [minDep, setMinDep] = useState(initial?.min_deposit_amount ?? '0');
  const [maxDep, setMaxDep] = useState(initial?.max_deposit_amount ?? '');
  const [minWd, setMinWd] = useState(initial?.min_withdrawal_amount ?? '0');
  const [maxWd, setMaxWd] = useState(initial?.max_withdrawal_amount ?? '');

  const [notice, setNotice] = useState(initial?.notice_period_days ?? 0);
  const [maxMo, setMaxMo] = useState<string>(initial?.max_withdrawals_per_month != null ? String(initial.max_withdrawals_per_month) : '');
  const [partial, setPartial] = useState(initial?.partial_withdrawal_allowed ?? true);
  const [largeThresh, setLargeThresh] = useState(initial?.large_withdrawal_threshold ?? '');

  const [lockIn, setLockIn] = useState(initial?.lock_in_months ?? 0);
  const [defaultTerm, setDefaultTerm] = useState<string>(initial?.default_term_months != null ? String(initial.default_term_months) : '');
  const [maturityAction, setMaturityAction] = useState<MaturityAction>(initial?.maturity_action ?? 'none');

  const [eligibility, setEligibility] = useState<DepositEligibility>(initial?.eligibility ?? 'individuals');
  const [reqApproval, setReqApproval] = useState(initial?.requires_approval_to_open ?? false);

  const [winStart, setWinStart] = useState<string>(initial?.withdrawal_window_start_month != null ? String(initial.withdrawal_window_start_month) : '');
  const [winEnd, setWinEnd] = useState<string>(initial?.withdrawal_window_end_month != null ? String(initial.withdrawal_window_end_month) : '');

  const [maintFee, setMaintFee] = useState(initial?.maintenance_fee ?? '0');
  const [maintFreq, setMaintFreq] = useState<FeeFrequency>(initial?.maintenance_fee_frequency ?? 'none');
  const [earlyPct, setEarlyPct] = useState(initial?.early_withdrawal_penalty_pct ?? '0');
  const [belowFee, setBelowFee] = useState(initial?.below_min_balance_fee ?? '0');
  const [dormFee, setDormFee] = useState(initial?.dormancy_fee_monthly ?? '0');

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function payload(): Partial<DepositProduct> {
    // BOSA invariants: no partial withdrawals, no notice period.
    // The backend rejects violations too (defence in depth) but the
    // client cleans the values so editing a BOSA product can't
    // accidentally re-introduce a stale FOSA setting from the
    // form state.
    const partialEff = isBOSA ? false : partial;
    const noticeEff = isBOSA ? 0 : notice;
    return {
      code: code.toUpperCase().trim(),
      name: name.trim(),
      product_type: productType,
      // Only send segment on create — it's immutable post-create
      // and the backend strips it on update anyway, but sending it
      // would force the caller to think it can be edited.
      ...(isNew && segmentEnabled ? { segment } : {}),
      description: desc || undefined,
      is_active: isActive,
      min_opening_balance: minOpen,
      min_operating_balance: minOp,
      max_balance: maxBal || undefined,
      min_deposit_amount: minDep,
      max_deposit_amount: maxDep || undefined,
      min_withdrawal_amount: minWd,
      max_withdrawal_amount: maxWd || undefined,
      notice_period_days: noticeEff,
      max_withdrawals_per_month: maxMo ? parseInt(maxMo, 10) : undefined,
      partial_withdrawal_allowed: partialEff,
      large_withdrawal_threshold: largeThresh || undefined,
      lock_in_months: lockIn,
      default_term_months: defaultTerm ? parseInt(defaultTerm, 10) : undefined,
      maturity_action: maturityAction,
      eligibility,
      requires_approval_to_open: reqApproval,
      withdrawal_window_start_month: winStart ? parseInt(winStart, 10) : undefined,
      withdrawal_window_end_month: winEnd ? parseInt(winEnd, 10) : undefined,
      maintenance_fee: maintFee,
      maintenance_fee_frequency: maintFreq,
      early_withdrawal_penalty_pct: earlyPct,
      below_min_balance_fee: belowFee,
      dormancy_fee_monthly: dormFee,
      required_monthly_amount: isBOSA ? reqMonthly : '0',
      required_day_of_month: isBOSA && reqDay ? parseInt(reqDay, 10) : undefined,
    };
  }

  async function submit() {
    setErr(null); setBusy(true);
    try {
      if (isNew) { await createDepositProduct(payload()); }
      else { await updateDepositProduct(initial!.id, payload()); }
      onSaved();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function archive() {
    if (!initial) return;
    if (!confirm(`Archive product ${initial.name}? Existing accounts remain usable but no new accounts can be opened.`)) return;
    setBusy(true);
    try {
      await updateDepositProduct(initial.id, { is_active: false });
      onSaved();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function destroy() {
    if (!initial) return;
    if (!confirm(`Delete product ${initial.name}? Only allowed when no accounts reference it.`)) return;
    setBusy(true);
    try {
      await deleteDepositProduct(initial.id);
      onSaved();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 720, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{isNew ? 'New deposit product' : `Edit · ${initial!.name}`}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">
          {err && <div className="alert alert-error">{err}</div>}

          {segmentEnabled && (
            <Field
              label="Segment"
              hint={isNew
                ? 'BOSA = member-deposit bond (secures loans, not withdrawable). FOSA = withdrawable savings.'
                : 'Immutable after creation.'}
            >
              <div style={{ display: 'flex', gap: 16 }}>
                {(['bosa', 'fosa'] as DepositSegment[]).map((s) => (
                  <label key={s} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                    <input
                      type="radio"
                      name="segment"
                      disabled={!isNew}
                      checked={segment === s}
                      onChange={() => {
                        setSegment(s);
                        // Re-anchor product_type to a valid option for the
                        // newly-chosen segment so the dropdown below never
                        // shows an invalid pair.
                        const valid = s === 'bosa' ? BOSA_TYPES : FOSA_TYPES;
                        if (!valid.includes(productType)) {
                          setProductType(valid[0]);
                        }
                      }}
                    />
                    <span>{s === 'bosa' ? 'BOSA · Member deposits' : 'FOSA · Withdrawable savings'}</span>
                  </label>
                ))}
              </div>
            </Field>
          )}

          <div className="grid-2" style={{ gap: 10 }}>
            <Field label="Code" hint={isNew ? 'Uppercase, no spaces. Immutable after creation.' : 'Immutable.'}>
              <input className="input mono" value={code} disabled={!isNew} onChange={(e) => setCode(e.target.value)} placeholder="ORD" />
            </Field>
            <Field label="Type" hint={isNew ? undefined : 'Immutable after creation.'}>
              <select className="input" disabled={!isNew} value={productType} onChange={(e) => setProductType(e.target.value as DepositProductType)}>
                {(segmentEnabled && isBOSA ? BOSA_TYPES : FOSA_TYPES).map((p) => (
                  <option key={p} value={p}>{PRODUCT_LABEL[p]}</option>
                ))}
              </select>
            </Field>
            <Field label="Name">
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
            <Field label="Status">
              <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                <input type="checkbox" checked={isActive} onChange={(e) => setIsActive(e.target.checked)} />
                <span>{isActive ? 'Active — members can open accounts' : 'Archived — existing accounts only'}</span>
              </label>
            </Field>
          </div>
          <Field label="Description">
            <textarea className="input" rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} />
          </Field>

          {isBOSA && (
            <>
              <h4 style={{ marginTop: 12 }}>Required contributions</h4>
              <div className="grid-2">
                <Field
                  label={`Required monthly amount (${currency})`}
                  hint="Members owe this every month; missed contributions surface in the Collection Desk's suggest-from-outstanding list."
                >
                  <input className="input mono" value={reqMonthly} onChange={(e) => setReqMonthly(e.target.value)} />
                </Field>
                <Field
                  label="Required day of month (1–28)"
                  hint="Capped at 28 so every month has the same anchor day — no Feb / 30-vs-31 special cases."
                >
                  <input
                    className="input mono"
                    type="number"
                    min={1}
                    max={28}
                    value={reqDay}
                    onChange={(e) => setReqDay(e.target.value)}
                    placeholder="5"
                  />
                </Field>
              </div>
            </>
          )}

          <h4 style={{ marginTop: 12 }}>Balance rules ({currency})</h4>
          <div className="grid-3">
            <Field label="Min opening"><input className="input mono" value={minOpen} onChange={(e) => setMinOpen(e.target.value)} /></Field>
            <Field label="Min operating"><input className="input mono" value={minOp} onChange={(e) => setMinOp(e.target.value)} /></Field>
            <Field label="Max balance (blank = uncapped)"><input className="input mono" value={maxBal} onChange={(e) => setMaxBal(e.target.value)} /></Field>
            <Field label="Min deposit"><input className="input mono" value={minDep} onChange={(e) => setMinDep(e.target.value)} /></Field>
            <Field label="Max deposit (blank = unbounded)"><input className="input mono" value={maxDep} onChange={(e) => setMaxDep(e.target.value)} /></Field>
            <Field label="Min withdrawal"><input className="input mono" value={minWd} onChange={(e) => setMinWd(e.target.value)} /></Field>
            <Field label="Max withdrawal (blank = unbounded)"><input className="input mono" value={maxWd} onChange={(e) => setMaxWd(e.target.value)} /></Field>
            <Field label="Large-withdrawal threshold" hint="> this routes via approval workflow (Phase 4).">
              <input className="input mono" value={largeThresh} onChange={(e) => setLargeThresh(e.target.value)} />
            </Field>
          </div>

          {/* BOSA accounts forbid partial withdrawals + notice
              periods + windows + lock-in by definition; hide those
              sections so the form doesn't suggest configurations the
              backend will reject. */}
          {!isBOSA && (
            <>
              <h4 style={{ marginTop: 12 }}>Withdrawal rules</h4>
              <div className="grid-3">
                <Field label="Notice period (days)"><input className="input mono" type="number" min={0} value={notice} onChange={(e) => setNotice(parseInt(e.target.value, 10) || 0)} /></Field>
                <Field label="Max withdrawals / month"><input className="input mono" value={maxMo} onChange={(e) => setMaxMo(e.target.value)} placeholder="blank = unlimited" /></Field>
                <Field label="Partial withdrawals">
                  <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                    <input type="checkbox" checked={partial} onChange={(e) => setPartial(e.target.checked)} />
                    <span>{partial ? 'Allowed' : 'Full-balance withdrawal only'}</span>
                  </label>
                </Field>
              </div>
            </>
          )}

          {!isBOSA && (productType === 'holiday' || productType === 'emergency') && (
            <div className="grid-2">
              <Field label="Withdrawal window — start month (1-12)">
                <input className="input mono" value={winStart} onChange={(e) => setWinStart(e.target.value)} placeholder="11" />
              </Field>
              <Field label="Withdrawal window — end month (1-12)">
                <input className="input mono" value={winEnd} onChange={(e) => setWinEnd(e.target.value)} placeholder="12" />
              </Field>
            </div>
          )}

          {!isBOSA && (productType === 'fixed' || productType === 'goal') && (
            <>
              <h4 style={{ marginTop: 12 }}>Lock-in</h4>
              <div className="grid-3">
                <Field label="Lock-in months">
                  <input className="input mono" type="number" min={0} value={lockIn} onChange={(e) => setLockIn(parseInt(e.target.value, 10) || 0)} />
                </Field>
                {productType === 'fixed' && (
                  <>
                    <Field label="Default term (months)"><input className="input mono" value={defaultTerm} onChange={(e) => setDefaultTerm(e.target.value)} /></Field>
                    <Field label="Maturity action">
                      <select className="input" value={maturityAction} onChange={(e) => setMaturityAction(e.target.value as MaturityAction)}>
                        <option value="none">No automatic action</option>
                        <option value="auto_renew">Auto-renew at same term</option>
                        <option value="liquidate_to_ordinary">Liquidate to ordinary savings</option>
                        <option value="notify">Notify member</option>
                      </select>
                    </Field>
                  </>
                )}
              </div>
            </>
          )}

          <h4 style={{ marginTop: 12 }}>Eligibility</h4>
          <div className="grid-2">
            <Field label="Who can open">
              <select className="input" value={eligibility} onChange={(e) => setEligibility(e.target.value as DepositEligibility)}>
                <option value="individuals">Individuals only</option>
                <option value="groups">Groups only</option>
                <option value="minors">Minors only (junior accounts)</option>
                <option value="all">All</option>
              </select>
            </Field>
            <Field label="Requires opening approval">
              <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                <input type="checkbox" checked={reqApproval} onChange={(e) => setReqApproval(e.target.checked)} />
                <span>{reqApproval ? 'Approval required' : 'Open directly'}</span>
              </label>
            </Field>
          </div>

          <h4 style={{ marginTop: 12 }}>Charges</h4>
          <div className="grid-3">
            <Field label={`Maintenance fee (${currency})`}><input className="input mono" value={maintFee} onChange={(e) => setMaintFee(e.target.value)} /></Field>
            <Field label="Maintenance frequency">
              <select className="input" value={maintFreq} onChange={(e) => setMaintFreq(e.target.value as FeeFrequency)}>
                <option value="none">No fee</option>
                <option value="monthly">Monthly</option>
                <option value="quarterly">Quarterly</option>
                <option value="annual">Annual</option>
              </select>
            </Field>
            <Field label="Early withdrawal penalty (%)"><input className="input mono" value={earlyPct} onChange={(e) => setEarlyPct(e.target.value)} /></Field>
            <Field label={`Below-min-balance fee (${currency})`}><input className="input mono" value={belowFee} onChange={(e) => setBelowFee(e.target.value)} /></Field>
            <Field label={`Dormancy fee / month (${currency})`}><input className="input mono" value={dormFee} onChange={(e) => setDormFee(e.target.value)} /></Field>
          </div>
        </div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'space-between', borderTop: '1px solid var(--border)' }}>
          <div style={{ display: 'flex', gap: 8 }}>
            {!isNew && initial?.is_active && (
              <button className="btn btn-sm" disabled={busy} onClick={() => void archive()}>Archive</button>
            )}
            {!isNew && (
              <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy} onClick={() => void destroy()}>Delete</button>
            )}
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="btn" onClick={onClose}>Cancel</button>
            <button className="btn btn-accent" disabled={busy || !code || !name} onClick={() => void submit()}>{busy ? 'Saving…' : 'Save'}</button>
          </div>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children, hint }: { label: string; children: React.ReactNode; hint?: string }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}
