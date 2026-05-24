// Loan product configuration — list + create/edit modal + purpose categories.
//
// The product form is large; we group fields into sections and surface
// the most-used controls first. Code + category are immutable post-create.

import { useEffect, useState, type ReactNode } from 'react';
import {
  createLoanProduct,
  createLoanPurposeCategory,
  deleteLoanProduct,
  extractError,
  isLegacyMultiplierBasis,
  listLoanProducts,
  listLoanPurposeCategories,
  updateLoanProduct,
  type LoanCategory,
  type LoanCollateralRequirement,
  type LoanFeeTiming,
  type LoanProductFee,
  type LoanInterestMethod,
  type LoanMultiplierBasis,
  type LoanProduct,
  type LoanPurposeCategory,
  type LoanRepaymentMethod,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge } from '../components/Badge';
import { Icon } from '../components/Icon';

const CATEGORY_LABEL: Record<LoanCategory, string> = {
  short_term: 'Short-term',
  medium_term: 'Medium-term',
  long_term: 'Long-term',
  emergency: 'Emergency',
  asset_finance: 'Asset finance',
  group: 'Group',
};

export default function LoanProductsPage() {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const canEdit = hasPermission('loans:configure');

  const [products, setProducts] = useState<LoanProduct[] | null>(null);
  const [purposes, setPurposes] = useState<LoanPurposeCategory[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<LoanProduct | 'new' | null>(null);
  const [purposeOpen, setPurposeOpen] = useState(false);

  async function reload() {
    setErr(null);
    try {
      const [p, c] = await Promise.all([
        listLoanProducts(true),
        listLoanPurposeCategories(true),
      ]);
      setProducts(p);
      setPurposes(c);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Lending · Configuration</div>
          <h1>Loan products</h1>
          <div className="page-sub">Define the loan types members can apply for and the rules each one enforces.</div>
        </div>
        <div className="page-hd-actions">
          {canEdit && (
            <>
              <button className="btn btn-sm" onClick={() => setPurposeOpen(true)}><Icon name="settings" size={12} /> Purposes</button>
              <button className="btn btn-sm btn-accent" onClick={() => setEditing('new')}><Icon name="plus" size={12} /> New product</button>
            </>
          )}
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {/* PR 2: surface tenants whose loan products still use the
          pre-BOSA/FOSA multiplier bases. The scorer silently routes
          those to BOSA-only when the tenant flag is on (and emits a
          per-application warning), but the admin banner here is the
          system-wide nudge to actually rebase the product. */}
      {products && products.filter((p) => isLegacyMultiplierBasis(p.multiplier_basis)).length > 0 && (
        <div className="alert alert-warn" style={{ marginBottom: 12 }}>
          <strong>{products.filter((p) => isLegacyMultiplierBasis(p.multiplier_basis)).length} product(s)</strong>
          {' '}use the legacy 'deposits' / 'shares + deposits' multiplier basis, which sums withdrawable savings (FOSA).
          {' '}This is not SACCO-prudential-safe. Edit each affected product to use 'BOSA' or 'BOSA + shares'.
        </div>
      )}

      <div className="card">
        <div className="card-hd">
          <h3>Catalog</h3>
          <span className="card-sub">{products?.length ?? 0} products</span>
        </div>
        <div className="card-body flush">
          {!products && <div className="empty">Loading…</div>}
          {products && products.length === 0 && (
            <div className="empty">
              No loan products yet.{' '}
              {canEdit && <a style={{ color: 'var(--accent)' }} onClick={() => setEditing('new')}>Create the first one →</a>}
            </div>
          )}
          {products && products.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Name</th>
                  <th>Category</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Range</th>
                  <th>Rate</th>
                  <th>Multiplier</th>
                  <th>Guarantors</th>
                  <th>Fees</th>
                  <th>Auto-approve</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {products.map((p) => (
                  <tr key={p.id}>
                    <td className="tiny-mono">{p.code}</td>
                    <td>{p.name}</td>
                    <td><Badge tone="accent">{CATEGORY_LABEL[p.category]}</Badge></td>
                    <td>{p.is_active ? <Badge tone="pos">active</Badge> : <Badge tone="neutral">archived</Badge>}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {currency} {fmt(p.min_amount)} – {fmt(p.max_amount)}
                    </td>
                    <td className="tiny">
                      {p.interest_rate_pct}% <span className="muted">{p.interest_method === 'flat_rate' ? 'flat' : 'reducing'}</span>
                      <div className="muted tiny">{p.min_term_months}-{p.max_term_months}m</div>
                    </td>
                    <td className="tiny">
                      {p.multiplier_basis === 'none' || !p.multiplier_value
                        ? '—'
                        : (
                          <>
                            {p.multiplier_value}× {p.multiplier_basis.replace(/_/g, ' ')}
                            {isLegacyMultiplierBasis(p.multiplier_basis) && (
                              <Badge tone="warn">legacy</Badge>
                            )}
                          </>
                        )}
                    </td>
                    <td className="mono tiny">{p.min_guarantors}</td>
                    <td className="tiny">
                      <FeeSummary p={p} currency={currency} />
                    </td>
                    <td className="tiny">
                      {p.auto_approval_threshold
                        ? <span>≤ {currency} {fmt(p.auto_approval_threshold)}{p.auto_approval_min_score != null && <span className="muted"> · score ≥ {p.auto_approval_min_score}</span>}</span>
                        : <span className="muted">—</span>}
                    </td>
                    <td>{canEdit && <button className="btn btn-sm" onClick={() => setEditing(p)}>Edit</button>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {purposes.length > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Purpose categories</h3>
            <span className="card-sub">{purposes.length} configured</span>
          </div>
          <div className="card-body">
            <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
              {purposes.map((p) => (
                <Badge key={p.id} tone={p.is_active ? 'accent' : 'neutral'}>
                  {p.code} · {p.name}
                </Badge>
              ))}
            </div>
          </div>
        </div>
      )}

      {editing && canEdit && (
        <ProductModal
          initial={editing === 'new' ? null : editing}
          currency={currency}
          onClose={() => setEditing(null)}
          onSaved={async () => { setEditing(null); await reload(); }}
        />
      )}
      {purposeOpen && canEdit && (
        <PurposesModal
          existing={purposes}
          onClose={() => setPurposeOpen(false)}
          onSaved={async () => { setPurposeOpen(false); await reload(); }}
        />
      )}
    </div>
  );
}

// ─────────── Product modal ───────────

function ProductModal({ initial, currency, onClose, onSaved }: {
  initial: LoanProduct | null;
  currency: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const isNew = initial === null;
  const [code, setCode] = useState(initial?.code ?? '');
  const [name, setName] = useState(initial?.name ?? '');
  const [category, setCategory] = useState<LoanCategory>(initial?.category ?? 'medium_term');
  const [desc, setDesc] = useState(initial?.description ?? '');
  const [isActive, setIsActive] = useState(initial?.is_active ?? true);

  const [minAmount, setMinAmount] = useState(initial?.min_amount ?? '0');
  const [maxAmount, setMaxAmount] = useState(initial?.max_amount ?? '100000');
  const [multBasis, setMultBasis] = useState<LoanMultiplierBasis>(initial?.multiplier_basis ?? 'none');
  const [multValue, setMultValue] = useState(initial?.multiplier_value ?? '');

  const [minTerm, setMinTerm] = useState(initial?.min_term_months ?? 1);
  const [maxTerm, setMaxTerm] = useState(initial?.max_term_months ?? 12);
  const [defaultTerm, setDefaultTerm] = useState<string>(initial?.default_term_months != null ? String(initial.default_term_months) : '');
  const [grace, setGrace] = useState(initial?.grace_period_months ?? 0);

  const [rate, setRate] = useState(initial?.interest_rate_pct ?? '12.0');
  const [interestMethod, setInterestMethod] = useState<LoanInterestMethod>(initial?.interest_method ?? 'reducing_balance');
  const [repayMethod, setRepayMethod] = useState<LoanRepaymentMethod>(initial?.repayment_method ?? 'reducing_balance');

  // Free-form fee list. Pre-populated from initial.fees on edit. New
  // products start empty — the SACCO adds whatever fees they actually
  // charge.
  const [fees, setFees] = useState<LoanProductFee[]>(
    initial?.fees && initial.fees.length > 0
      ? initial.fees.map((f) => ({ ...f }))
      : [],
  );
  const [penaltyRate, setPenaltyRate] = useState(initial?.penalty_rate_pct ?? '0');

  const [minGuar, setMinGuar] = useState(initial?.min_guarantors ?? 0);
  const [maxGuarExp, setMaxGuarExp] = useState(initial?.max_guarantor_exposure_pct ?? '100');
  const [guarMember, setGuarMember] = useState(initial?.guarantor_must_be_member ?? true);
  const [collateralReq, setCollateralReq] = useState<LoanCollateralRequirement>(initial?.collateral_requirement ?? 'not_applicable');

  const [minMembership, setMinMembership] = useState(initial?.min_membership_months ?? 0);
  const [minShares, setMinShares] = useState(initial?.min_shares_required ?? 0);
  const [allowConcurrent, setAllowConcurrent] = useState(initial?.allow_concurrent ?? false);

  const [autoThresh, setAutoThresh] = useState(initial?.auto_approval_threshold ?? '');
  const [autoMinScore, setAutoMinScore] = useState<string>(initial?.auto_approval_min_score != null ? String(initial.auto_approval_min_score) : '');

  const [allowTopup, setAllowTopup] = useState(initial?.allow_topup ?? false);
  const [allowRefinance, setAllowRefinance] = useState(initial?.allow_refinance ?? false);

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function payload(): Partial<LoanProduct> {
    return {
      code: code.toUpperCase().trim(),
      name: name.trim(),
      category,
      description: desc || undefined,
      is_active: isActive,
      min_amount: minAmount,
      max_amount: maxAmount,
      multiplier_basis: multBasis,
      multiplier_value: multBasis !== 'none' && multValue ? multValue : undefined,
      min_term_months: minTerm,
      max_term_months: maxTerm,
      default_term_months: defaultTerm ? parseInt(defaultTerm, 10) : undefined,
      grace_period_months: grace,
      interest_rate_pct: rate,
      interest_method: interestMethod,
      repayment_method: repayMethod,
      fees: fees.map((f, i) => ({
        name: f.name.trim(),
        amount: f.amount,
        is_pct: f.is_pct,
        timing: f.timing,
        display_order: i + 1,
      })),
      penalty_rate_pct: penaltyRate,
      min_guarantors: minGuar,
      max_guarantor_exposure_pct: maxGuarExp,
      guarantor_must_be_member: guarMember,
      collateral_requirement: collateralReq,
      min_membership_months: minMembership,
      min_shares_required: minShares,
      allow_concurrent: allowConcurrent,
      auto_approval_threshold: autoThresh || undefined,
      auto_approval_min_score: autoMinScore ? parseInt(autoMinScore, 10) : undefined,
      allow_topup: allowTopup,
      allow_refinance: allowRefinance,
    };
  }

  async function submit() {
    setErr(null); setBusy(true);
    try {
      if (isNew) await createLoanProduct(payload());
      else await updateLoanProduct(initial!.id, payload());
      onSaved();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function archive() {
    if (!initial) return;
    if (!confirm(`Archive ${initial.name}? Existing applications remain usable; no new applications can be created.`)) return;
    setBusy(true);
    try { await updateLoanProduct(initial.id, { is_active: false }); onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  async function destroy() {
    if (!initial) return;
    if (!confirm(`Delete ${initial.name}? Only allowed if no applications reference this product.`)) return;
    setBusy(true);
    try { await deleteLoanProduct(initial.id); onSaved(); }
    catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 780, maxWidth: '94vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{isNew ? 'New loan product' : `Edit · ${initial!.name}`}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">
          {err && <div className="alert alert-error">{err}</div>}
          <Section title="Identity">
            <div className="grid-2">
              <Field label="Code" hint={isNew ? 'Uppercase, no spaces. Immutable.' : 'Immutable.'}>
                <input className="input mono" disabled={!isNew} value={code} onChange={(e) => setCode(e.target.value)} placeholder="NORMAL" />
              </Field>
              <Field label="Category" hint={isNew ? undefined : 'Immutable.'}>
                <select className="input" disabled={!isNew} value={category} onChange={(e) => setCategory(e.target.value as LoanCategory)}>
                  {(['short_term', 'medium_term', 'long_term', 'emergency', 'asset_finance', 'group'] as LoanCategory[]).map((c) =>
                    <option key={c} value={c}>{CATEGORY_LABEL[c]}</option>)}
                </select>
              </Field>
              <Field label="Name"><input className="input" value={name} onChange={(e) => setName(e.target.value)} /></Field>
              <Field label="Active">
                <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  <input type="checkbox" checked={isActive} onChange={(e) => setIsActive(e.target.checked)} />
                  <span>{isActive ? 'Live — accepting applications' : 'Archived'}</span>
                </label>
              </Field>
            </div>
            <Field label="Description (optional)">
              <textarea className="input" rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} />
            </Field>
          </Section>

          <Section title={`Amount limits (${currency})`}>
            <div className="grid-3">
              <Field label="Minimum amount"><input className="input mono" value={minAmount} onChange={(e) => setMinAmount(e.target.value)} /></Field>
              <Field label="Maximum amount"><input className="input mono" value={maxAmount} onChange={(e) => setMaxAmount(e.target.value)} /></Field>
              <Field
                label="Multiplier basis"
                hint={isLegacyMultiplierBasis(multBasis)
                  ? "Deprecated: this basis sums withdrawable savings (FOSA), which doesn't meet SACCO prudential practice. Switch to 'bosa' or 'bosa + shares' so the ceiling is computed off the non-withdrawable bond."
                  : undefined}
              >
                <select className="input" value={multBasis} onChange={(e) => setMultBasis(e.target.value as LoanMultiplierBasis)}>
                  <option value="none">None (use absolute max)</option>
                  <option value="shares">×  shares</option>
                  <option value="bosa">×  BOSA (member deposits)</option>
                  <option value="bosa_plus_shares">×  BOSA + shares</option>
                  <option value="deposits" disabled={!isLegacyMultiplierBasis(multBasis)}>×  deposits (legacy)</option>
                  <option value="shares_plus_deposits" disabled={!isLegacyMultiplierBasis(multBasis)}>×  shares + deposits (legacy)</option>
                </select>
              </Field>
              {multBasis !== 'none' && (
                <Field label={`Multiplier value (e.g. 3 = "3× ${multBasis.replace(/_/g, ' ')}")`}>
                  <input className="input mono" value={multValue} onChange={(e) => setMultValue(e.target.value)} />
                </Field>
              )}
            </div>
          </Section>

          <Section title="Tenure">
            <div className="grid-3">
              <Field label="Min term (months)"><input className="input mono" type="number" min={1} value={minTerm} onChange={(e) => setMinTerm(parseInt(e.target.value, 10) || 0)} /></Field>
              <Field label="Max term (months)"><input className="input mono" type="number" min={1} value={maxTerm} onChange={(e) => setMaxTerm(parseInt(e.target.value, 10) || 0)} /></Field>
              <Field label="Default term (optional)"><input className="input mono" value={defaultTerm} onChange={(e) => setDefaultTerm(e.target.value)} /></Field>
              <Field label="Grace period (months)" hint="Number of months before first repayment is due."><input className="input mono" type="number" min={0} value={grace} onChange={(e) => setGrace(parseInt(e.target.value, 10) || 0)} /></Field>
            </div>
          </Section>

          <Section title="Interest">
            <div className="grid-3">
              <Field label="Rate (% p.a.)"><input className="input mono" value={rate} onChange={(e) => setRate(e.target.value)} /></Field>
              <Field label="Interest method">
                <select className="input" value={interestMethod} onChange={(e) => setInterestMethod(e.target.value as LoanInterestMethod)}>
                  <option value="reducing_balance">Reducing balance</option>
                  <option value="flat_rate">Flat rate</option>
                </select>
              </Field>
              <Field label="Repayment schedule">
                <select className="input" value={repayMethod} onChange={(e) => setRepayMethod(e.target.value as LoanRepaymentMethod)}>
                  <option value="reducing_balance">Amortising (equal installments)</option>
                  <option value="flat_rate">Flat-rate (equal installments)</option>
                  <option value="bullet">Bullet (one payment at maturity)</option>
                  <option value="interest_only">Interest-only (principal at maturity)</option>
                </select>
              </Field>
              <Field label="Penalty rate (% / month on overdue principal)"><input className="input mono" value={penaltyRate} onChange={(e) => setPenaltyRate(e.target.value)} /></Field>
            </div>
          </Section>

          <Section title={`Fees (${currency})`}>
            <p className="muted tiny" style={{ marginTop: 0 }}>
              Define any fees this product charges (Processing, Insurance / LPF, Appraisal, CRB filing, SMS notification, etc.).
              You can have zero, one, or many. Remove any you don't want — none are required.
            </p>
            {fees.length === 0 && (
              <p className="muted" style={{ margin: '8px 0', padding: 10, background: 'var(--surface-2)', border: '1px dashed var(--border)', borderRadius: 4 }}>
                No fees configured. The full principal will be disbursed.
              </p>
            )}
            {fees.map((f, idx) => (
              <FeeEditorRow
                key={idx}
                fee={f}
                onChange={(next) => setFees((arr) => arr.map((x, i) => i === idx ? next : x))}
                onRemove={() => setFees((arr) => arr.filter((_, i) => i !== idx))}
                onMoveUp={idx > 0 ? () => setFees((arr) => swapAt(arr, idx, idx - 1)) : undefined}
                onMoveDown={idx < fees.length - 1 ? () => setFees((arr) => swapAt(arr, idx, idx + 1)) : undefined}
              />
            ))}
            <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
              <button type="button" className="btn btn-sm" onClick={() => setFees((arr) => [...arr, {
                name: '', amount: '0', is_pct: false, timing: 'upfront' as LoanFeeTiming, display_order: arr.length + 1,
              }])}>+ Add fee</button>
              {[
                { name: 'Processing fee', amount: '2.5', is_pct: true, timing: 'upfront' as LoanFeeTiming },
                { name: 'Insurance / LPF fee', amount: '1', is_pct: true, timing: 'upfront' as LoanFeeTiming },
                { name: 'Appraisal fee', amount: '500', is_pct: false, timing: 'upfront' as LoanFeeTiming },
                { name: 'CRB filing fee', amount: '150', is_pct: false, timing: 'upfront' as LoanFeeTiming },
              ]
                .filter((preset) => !fees.some((f) => f.name.trim().toLowerCase() === preset.name.toLowerCase()))
                .map((preset) => (
                  <button key={preset.name} type="button" className="btn btn-sm btn-ghost"
                    title={`Quick-add ${preset.name}`}
                    onClick={() => setFees((arr) => [...arr, {
                      ...preset, display_order: arr.length + 1,
                    }])}>
                    + {preset.name}
                  </button>
                ))}
            </div>
          </Section>

          <Section title="Eligibility">
            <div className="grid-3">
              <Field label="Min membership (months)"><input className="input mono" type="number" min={0} value={minMembership} onChange={(e) => setMinMembership(parseInt(e.target.value, 10) || 0)} /></Field>
              <Field label="Min shares required"><input className="input mono" type="number" min={0} value={minShares} onChange={(e) => setMinShares(parseInt(e.target.value, 10) || 0)} /></Field>
              <Field label="Concurrent loans">
                <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  <input type="checkbox" checked={allowConcurrent} onChange={(e) => setAllowConcurrent(e.target.checked)} />
                  <span>{allowConcurrent ? 'Allowed' : 'Only one of this product at a time'}</span>
                </label>
              </Field>
            </div>
          </Section>

          <Section title="Guarantors & collateral">
            <div className="grid-3">
              <Field label="Min guarantors"><input className="input mono" type="number" min={0} value={minGuar} onChange={(e) => setMinGuar(parseInt(e.target.value, 10) || 0)} /></Field>
              <Field label="Max per-guarantor exposure (% of their shares+deposits)"><input className="input mono" value={maxGuarExp} onChange={(e) => setMaxGuarExp(e.target.value)} /></Field>
              <Field label="Guarantors must be members">
                <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  <input type="checkbox" checked={guarMember} onChange={(e) => setGuarMember(e.target.checked)} />
                  <span>{guarMember ? 'Required' : 'Not required'}</span>
                </label>
              </Field>
              <Field label="Collateral">
                <select className="input" value={collateralReq} onChange={(e) => setCollateralReq(e.target.value as LoanCollateralRequirement)}>
                  <option value="not_applicable">Not applicable</option>
                  <option value="optional">Optional</option>
                  <option value="required">Required</option>
                </select>
              </Field>
            </div>
          </Section>

          <Section title="Auto-approval (optional)">
            <div className="grid-2">
              <Field label={`Threshold — auto-approve below (${currency})`} hint="Leave blank to require manual approval for all applications.">
                <input className="input mono" value={autoThresh} onChange={(e) => setAutoThresh(e.target.value)} />
              </Field>
              <Field label="Minimum credit score for auto-approval">
                <input className="input mono" value={autoMinScore} onChange={(e) => setAutoMinScore(e.target.value)} placeholder="e.g. 70" />
              </Field>
            </div>
            <div className="grid-2">
              <Field label="Allow top-up">
                <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  <input type="checkbox" checked={allowTopup} onChange={(e) => setAllowTopup(e.target.checked)} />
                  <span>{allowTopup ? 'Members can top up active loans' : 'No top-ups'}</span>
                </label>
              </Field>
              <Field label="Allow refinance">
                <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  <input type="checkbox" checked={allowRefinance} onChange={(e) => setAllowRefinance(e.target.checked)} />
                  <span>{allowRefinance ? 'Refinancing permitted' : 'No refinancing'}</span>
                </label>
              </Field>
            </div>
          </Section>
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

function swapAt<T>(arr: T[], i: number, j: number): T[] {
  const out = arr.slice();
  [out[i], out[j]] = [out[j], out[i]];
  return out;
}

function FeeEditorRow({ fee, onChange, onRemove, onMoveUp, onMoveDown }: {
  fee: LoanProductFee;
  onChange: (next: LoanProductFee) => void;
  onRemove: () => void;
  onMoveUp?: () => void;
  onMoveDown?: () => void;
}) {
  return (
    <div style={{
      display: 'grid',
      gridTemplateColumns: '2fr 1.2fr 0.9fr 1.6fr auto',
      gap: 8, alignItems: 'flex-end', marginBottom: 8,
      padding: 8, background: 'var(--surface-2)', borderRadius: 4,
    }}>
      <Field label="Fee name">
        <input
          className="input"
          value={fee.name}
          onChange={(e) => onChange({ ...fee, name: e.target.value })}
          placeholder="e.g. Processing fee"
        />
      </Field>
      <Field label="Amount">
        <input
          className="input mono"
          value={fee.amount}
          onChange={(e) => onChange({ ...fee, amount: e.target.value })}
          placeholder="0.00"
        />
      </Field>
      <Field label="Type">
        <select className="input" value={fee.is_pct ? 'pct' : 'flat'}
          onChange={(e) => onChange({ ...fee, is_pct: e.target.value === 'pct' })}>
          <option value="pct">% of principal</option>
          <option value="flat">Flat amount</option>
        </select>
      </Field>
      <Field label="Timing">
        <select className="input" value={fee.timing}
          onChange={(e) => onChange({ ...fee, timing: e.target.value as LoanFeeTiming })}>
          <option value="upfront">Upfront (deduct at disbursement)</option>
          <option value="added_to_loan">Added to loan principal</option>
          <option value="at_each_installment">Charged each installment</option>
        </select>
      </Field>
      <div style={{ display: 'flex', gap: 4, paddingBottom: 10 }}>
        <button type="button" className="btn btn-sm btn-ghost" title="Move up" disabled={!onMoveUp} onClick={onMoveUp}>↑</button>
        <button type="button" className="btn btn-sm btn-ghost" title="Move down" disabled={!onMoveDown} onClick={onMoveDown}>↓</button>
        <button type="button" className="btn btn-sm" title="Remove this fee" onClick={onRemove} style={{ color: 'var(--neg)' }}>Remove</button>
      </div>
    </div>
  );
}

// Compact per-product fee chip strip rendered in the list view. Skips
// any fee whose amount is zero so an "all-zero" product reads as "—"
// rather than three cluttery zero rows.
const TIMING_TAG: Record<string, string> = {
  upfront: 'upfront',
  added_to_loan: '+loan',
  at_each_installment: '/inst',
};
// Short label for the chip strip — first word of the fee name plus a
// fallback to the full name if it's a single word. Keeps chips compact
// without losing intent.
function shortFeeLabel(name: string): string {
  const trimmed = name.trim();
  if (trimmed.length <= 14) return trimmed;
  const first = trimmed.split(/\s+/)[0];
  return first.length >= 3 ? first : trimmed.slice(0, 14) + '…';
}

function FeeSummary({ p, currency }: { p: LoanProduct; currency: string }) {
  const rows = (p.fees ?? []).filter((r) => parseFloat(r.amount) > 0);
  if (rows.length === 0) return <span className="muted">—</span>;
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
      {rows.map((r) => (
        <span key={r.id ?? r.name} className="mono tiny" title={`${r.name} · ${TIMING_TAG[r.timing] ?? r.timing}`}
          style={{
            padding: '1px 6px', borderRadius: 3,
            background: 'var(--surface-2)', border: '1px solid var(--border)',
          }}
        >
          {shortFeeLabel(r.name)} {r.is_pct ? `${r.amount}%` : `${currency} ${fmt(r.amount)}`}
          <span className="muted" style={{ marginLeft: 3 }}>({TIMING_TAG[r.timing] ?? r.timing})</span>
        </span>
      ))}
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 14, paddingBottom: 10, borderBottom: '1px solid var(--border)' }}>
      <h4 style={{ margin: '0 0 8px', fontSize: 13, textTransform: 'uppercase', letterSpacing: '.5px', color: 'var(--muted)' }}>{title}</h4>
      {children}
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

function fmt(s: string | number | undefined): string {
  if (s === undefined || s === null) return '0';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 0, maximumFractionDigits: 2 });
}

// ─────────── Purposes modal ───────────

function PurposesModal({ existing, onClose, onSaved }: {
  existing: LoanPurposeCategory[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [code, setCode] = useState('');
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function add() {
    setErr(null);
    if (!code.trim() || !name.trim()) { setErr('Code and name are required.'); return; }
    setBusy(true);
    try {
      await createLoanPurposeCategory({ code: code.toUpperCase().trim(), name: name.trim() });
      setCode(''); setName('');
      onSaved();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 480, maxWidth: '92vw' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>Loan purpose categories</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">
          {err && <div className="alert alert-error">{err}</div>}
          <p className="muted tiny" style={{ marginTop: 0 }}>Members pick from this list at application. Add what's relevant to your SACCO.</p>
          {existing.length > 0 && (
            <div style={{ marginBottom: 12 }}>
              <div className="muted tiny" style={{ marginBottom: 4 }}>Existing:</div>
              <div className="row" style={{ flexWrap: 'wrap', gap: 6 }}>
                {existing.map((p) => <Badge key={p.id} tone="accent">{p.code} · {p.name}</Badge>)}
              </div>
            </div>
          )}
          <Field label="New code"><input className="input mono" value={code} onChange={(e) => setCode(e.target.value)} placeholder="BIZ" /></Field>
          <Field label="New name"><input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Business expansion" /></Field>
        </div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Close</button>
          <button className="btn btn-accent" disabled={busy || !code || !name} onClick={() => void add()}>{busy ? 'Adding…' : 'Add purpose'}</button>
        </div>
      </div>
    </div>
  );
}
