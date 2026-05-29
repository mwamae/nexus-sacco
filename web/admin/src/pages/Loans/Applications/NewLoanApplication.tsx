// /loans/applications/new — loan application creation form.
//
// Form fields: member, product, requested amount + term, monthly
// income/expenses/obligations, optional purpose note, **guarantors**.
// The backend handler does the validation + initial scoring + (when
// the application is complete) transitions the row to pending_validation.
//
// Guarantors are required when the selected product's min_guarantors > 0.
// The original Phase 1 form sent guarantors: [] on the (wrong)
// assumption the backend permitted empty arrays at creation. The
// backend actually enforces MinGuarantors at Create time and returns
// "application has fewer guarantors than the product requires" — so
// the section had to be collected up-front. This fixes that.
//
// Collateral attachment remains deferred — officers add collateral
// via the detail page after creation. The backend allows zero collateral
// on creation for products where it's optional.
//
// Permission: loans:apply.

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../../auth/AuthContext';
import {
  createLoanApplication,
  listLoanProducts,
  listCounterparties,
  getGuarantorCapacity,
  type LoanProduct,
  type Counterparty,
  type GuarantorCapacity,
  extractError,
} from '../../../api/client';
import { useDocumentTitle } from '../../../lib/useDocumentTitle';

export default function NewLoanApplication() {
  useDocumentTitle('Loans · New application');
  const { hasPermission } = useAuth();
  const allowed = hasPermission('loans:apply');

  const [products, setProducts] = useState<LoanProduct[]>([]);
  const [members, setMembers] = useState<Counterparty[]>([]);

  // Form fields
  const [counterpartyID, setCounterpartyID] = useState('');
  const [productID, setProductID] = useState('');
  const [requestedAmount, setRequestedAmount] = useState('');
  const [requestedTerm, setRequestedTerm] = useState('12');
  const [purposeNote, setPurposeNote] = useState('');
  const [monthlyNetIncome, setMonthlyNetIncome] = useState('');
  const [monthlyExpenses, setMonthlyExpenses] = useState('');
  const [otherIncome, setOtherIncome] = useState('');
  const [monthlyObligations, setMonthlyObligations] = useState('');

  // Guarantor rows. Always at least one empty row visible when the
  // selected product requires guarantors, so the user sees a place
  // to add them. Empty rows are filtered out at submit.
  //
  // Self-guarantee is allowed (some SACCO products permit a member
  // to pledge their own deposits against their own loan); the picker
  // includes the applicant.
  type GuarantorRow = { counterparty_id: string; amount_guaranteed: string };
  const [guarantors, setGuarantors] = useState<GuarantorRow[]>([]);

  // Per-counterparty guarantor capacity cache. Populated lazily as
  // the operator picks guarantors so we don't fan-out queries against
  // every member up-front.
  const [capacityByCP, setCapacityByCP] = useState<Record<string, GuarantorCapacity>>({});
  const [capacityLoading, setCapacityLoading] = useState<Record<string, boolean>>({});

  async function fetchCapacity(cpID: string) {
    if (!cpID) return;
    if (capacityByCP[cpID]) return;
    if (capacityLoading[cpID]) return;
    setCapacityLoading((p) => ({ ...p, [cpID]: true }));
    try {
      const cap = await getGuarantorCapacity(cpID);
      setCapacityByCP((p) => ({ ...p, [cpID]: cap }));
    } catch {
      // Best-effort — capacity is a hint, not a blocker.
    } finally {
      setCapacityLoading((p) => ({ ...p, [cpID]: false }));
    }
  }

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Derive the active product + its guarantor requirement.
  const currentProduct = useMemo(
    () => products.find((p) => p.id === productID),
    [products, productID],
  );
  const requiredGuarantors = currentProduct?.min_guarantors ?? 0;

  // When the product changes, ensure the guarantor list has at least
  // `requiredGuarantors` rows visible (don't shrink — operator may
  // already have populated more than the minimum).
  useEffect(() => {
    if (requiredGuarantors > guarantors.length) {
      setGuarantors((prev) => {
        const next = [...prev];
        while (next.length < requiredGuarantors) next.push({ counterparty_id: '', amount_guaranteed: '' });
        return next;
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [requiredGuarantors]);

  function updateGuarantor(idx: number, patch: Partial<GuarantorRow>) {
    setGuarantors((prev) => prev.map((g, i) => (i === idx ? { ...g, ...patch } : g)));
  }
  function addGuarantor() {
    setGuarantors((prev) => [...prev, { counterparty_id: '', amount_guaranteed: '' }]);
  }
  function removeGuarantor(idx: number) {
    setGuarantors((prev) => prev.filter((_, i) => i !== idx));
  }

  useEffect(() => {
    if (!allowed) return;
    void (async () => {
      try {
        const [ps, ms] = await Promise.all([
          listLoanProducts(false),
          listCounterparties({ limit: 500 }),
        ]);
        setProducts(ps);
        setMembers(ms.counterparties ?? []);
      } catch (e) {
        setErr(extractError(e));
      }
    })();
  }, [allowed]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      // Filter empty guarantor rows; validate the remaining ones.
      const populated = guarantors.filter(
        (g) => g.counterparty_id !== '' || g.amount_guaranteed !== '',
      );
      for (const g of populated) {
        if (!g.counterparty_id) {
          throw new Error('Each guarantor row needs a member selected.');
        }
        const amt = parseFloat(g.amount_guaranteed);
        if (!isFinite(amt) || amt <= 0) {
          throw new Error('Each guarantor needs a positive amount.');
        }
      }
      if (populated.length < requiredGuarantors) {
        throw new Error(
          `This product requires at least ${requiredGuarantors} guarantor${requiredGuarantors === 1 ? '' : 's'}; you provided ${populated.length}.`,
        );
      }
      // Detect duplicate guarantors before the backend does.
      const seen = new Set<string>();
      for (const g of populated) {
        if (seen.has(g.counterparty_id)) {
          throw new Error('The same member appears more than once in the guarantor list.');
        }
        seen.add(g.counterparty_id);
      }

      const r = await createLoanApplication({
        counterparty_id: counterpartyID,
        product_id: productID,
        requested_amount: requestedAmount,
        requested_term_months: parseInt(requestedTerm, 10),
        purpose_note: purposeNote || undefined,
        monthly_net_income: monthlyNetIncome,
        monthly_expenses: monthlyExpenses,
        other_income: otherIncome || '0',
        monthly_existing_obligations: monthlyObligations || '0',
        guarantors: populated,
        collateral: [],
      });
      window.location.href = `/loans/applications/${r.application.id}`;
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>New loan application</h1></div>
        <div className="alert alert-warn">You need <code>loans:apply</code> permission.</div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/loans/applications" style={{ color: 'inherit' }}>← Applications</a>
          </div>
          <h1>New loan application</h1>
          <div className="page-sub">
            Phase 1 form — guarantor + collateral attachment land in Phase 2.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      <form onSubmit={submit} className="card">
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))', gap: 14 }}>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Member</div>
            <select
              className="input"
              value={counterpartyID}
              onChange={(e) => setCounterpartyID(e.target.value)}
              required
            >
              <option value="">— select member —</option>
              {members.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.cp_number} · {m.display_name}
                </option>
              ))}
            </select>
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Product</div>
            <select
              className="input"
              value={productID}
              onChange={(e) => setProductID(e.target.value)}
              required
            >
              <option value="">— select product —</option>
              {products.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.code} · {p.name}
                </option>
              ))}
            </select>
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Requested amount</div>
            <input
              className="input"
              type="number"
              step="0.01"
              min="0"
              value={requestedAmount}
              onChange={(e) => setRequestedAmount(e.target.value)}
              required
            />
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Requested term (months)</div>
            <input
              className="input"
              type="number"
              min="1"
              max="240"
              value={requestedTerm}
              onChange={(e) => setRequestedTerm(e.target.value)}
              required
            />
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Monthly net income</div>
            <input
              className="input"
              type="number"
              step="0.01"
              min="0"
              value={monthlyNetIncome}
              onChange={(e) => setMonthlyNetIncome(e.target.value)}
              required
            />
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Other income</div>
            <input
              className="input"
              type="number"
              step="0.01"
              min="0"
              value={otherIncome}
              onChange={(e) => setOtherIncome(e.target.value)}
            />
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Monthly expenses</div>
            <input
              className="input"
              type="number"
              step="0.01"
              min="0"
              value={monthlyExpenses}
              onChange={(e) => setMonthlyExpenses(e.target.value)}
              required
            />
          </label>

          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Existing monthly obligations</div>
            <input
              className="input"
              type="number"
              step="0.01"
              min="0"
              value={monthlyObligations}
              onChange={(e) => setMonthlyObligations(e.target.value)}
            />
          </label>

          <label style={{ gridColumn: '1 / -1' }}>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Purpose note</div>
            <textarea
              className="input"
              value={purposeNote}
              onChange={(e) => setPurposeNote(e.target.value)}
              rows={3}
              placeholder="What is the loan for?"
            />
          </label>
        </div>

        {/* ── Guarantors section ────────────────────────────────────
            Visible once a product is selected. Required count is
            driven by product.min_guarantors. */}
        {productID && (
          <div className="card-body" style={{ borderTop: '1px solid var(--border)' }}>
            <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginBottom: 8 }}>
              <h3 style={{ margin: 0 }}>Guarantors</h3>
              <div className="muted tiny">
                {requiredGuarantors === 0
                  ? 'Optional for this product'
                  : `Minimum ${requiredGuarantors} required · ${guarantors.filter((g) => g.counterparty_id !== '').length}/${requiredGuarantors} added`}
              </div>
            </div>

            {currentProduct?.guarantor_must_be_member && (
              <div className="muted tiny" style={{ marginBottom: 8 }}>
                Guarantors must be active SACCO members for this product.
              </div>
            )}

            {guarantors.length === 0 ? (
              <div className="muted tiny" style={{ padding: '6px 0' }}>
                No guarantors added yet. Click <strong>+ Add guarantor</strong> below.
              </div>
            ) : (
              <div style={{ display: 'grid', gap: 8 }}>
                {guarantors.map((g, idx) => {
                  // Picker excludes any member already selected in
                  // another row, so the operator can't double-list
                  // someone. Self-guarantee IS allowed — the applicant
                  // stays in the list.
                  const otherPicked = new Set(
                    guarantors.filter((_, i) => i !== idx).map((x) => x.counterparty_id).filter(Boolean),
                  );
                  const choices = members.filter((m) => !otherPicked.has(m.id));
                  const cap = g.counterparty_id ? capacityByCP[g.counterparty_id] : undefined;
                  const isSelf = g.counterparty_id && g.counterparty_id === counterpartyID;
                  const amtNum = parseFloat(g.amount_guaranteed || '0');
                  const capNum = cap ? parseFloat(cap.available_capacity) : NaN;
                  const exceedsCapacity = cap && amtNum > capNum;
                  return (
                    <div key={idx} style={{ display: 'grid', gap: 4 }}>
                      <div style={{ display: 'grid', gridTemplateColumns: '2fr 1fr auto', gap: 8, alignItems: 'center' }}>
                        <select
                          className="input"
                          value={g.counterparty_id}
                          onChange={(e) => {
                            updateGuarantor(idx, { counterparty_id: e.target.value });
                            void fetchCapacity(e.target.value);
                          }}
                        >
                          <option value="">— select guarantor —</option>
                          {/* If the row already has a member selected that isn't in the
                              filtered list (edge case), keep them visible so the value renders. */}
                          {g.counterparty_id && !choices.find((c) => c.id === g.counterparty_id) && (
                            <option value={g.counterparty_id}>
                              {members.find((m) => m.id === g.counterparty_id)?.display_name ?? g.counterparty_id}
                            </option>
                          )}
                          {choices.map((m) => (
                            <option key={m.id} value={m.id}>
                              {m.cp_number} · {m.display_name}{m.id === counterpartyID ? ' (self)' : ''}
                            </option>
                          ))}
                        </select>
                        <input
                          className="input"
                          type="number"
                          step="0.01"
                          min="0"
                          placeholder="Amount guaranteed"
                          value={g.amount_guaranteed}
                          onChange={(e) => updateGuarantor(idx, { amount_guaranteed: e.target.value })}
                          style={exceedsCapacity ? { borderColor: 'var(--warn, #c97a00)' } : undefined}
                        />
                        <button
                          type="button"
                          className="btn"
                          onClick={() => removeGuarantor(idx)}
                          disabled={guarantors.length <= requiredGuarantors && requiredGuarantors > 0}
                          title={guarantors.length <= requiredGuarantors && requiredGuarantors > 0
                            ? `Cannot remove — minimum ${requiredGuarantors} required`
                            : 'Remove this guarantor'}
                        >
                          ×
                        </button>
                      </div>

                      {/* Inline capacity hint — appears once a guarantor is picked. */}
                      {g.counterparty_id && (
                        <div className="muted tiny" style={{ paddingLeft: 4 }}>
                          {capacityLoading[g.counterparty_id] && 'Checking capacity…'}
                          {!capacityLoading[g.counterparty_id] && cap && (
                            <span>
                              {isSelf && <strong style={{ color: 'var(--accent, #2c5282)' }}>Self-guarantee · </strong>}
                              Available capacity: <strong>{cap.available_capacity}</strong>
                              {' '}(BOSA {cap.bosa_balance} − own loans {cap.own_loan_principal} − existing guarantees {cap.existing_guarantees}
                              {cap.active_guarantee_count > 0 && ` across ${cap.active_guarantee_count}`})
                              {exceedsCapacity && (
                                <span style={{ color: 'var(--warn, #c97a00)', marginLeft: 8 }}>
                                  ⚠ exceeds available capacity
                                </span>
                              )}
                            </span>
                          )}
                          {!capacityLoading[g.counterparty_id] && !cap && (
                            <span>Capacity lookup unavailable</span>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            )}

            <button
              type="button"
              className="btn btn-sm"
              style={{ marginTop: 10 }}
              onClick={addGuarantor}
            >
              + Add guarantor
            </button>
          </div>
        )}

        <div className="card-body" style={{ borderTop: '1px solid var(--border)', display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <a className="btn" href="/loans/applications">Cancel</a>
          <button className="btn btn-accent" type="submit" disabled={busy}>
            {busy ? 'Creating…' : 'Create application'}
          </button>
        </div>
      </form>
    </div>
  );
}
