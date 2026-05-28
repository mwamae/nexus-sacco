// /loans/applications/new — minimal loan application creation form.
//
// Phase 1 ships the smallest workable form: pick member, pick
// product, enter requested amount + term + monthly income + monthly
// expenses, optional purpose note. The backend handler does the
// validation + initial scoring + (when the application is complete)
// transitions the row to pending_validation.
//
// Guarantor + collateral attachment is deferred to Phase 2's richer
// form; the API allows zero of each on create, so Phase 1 just sends
// empty arrays. Members + officers can add guarantors via the
// detail page's Guarantors tab after creation.
//
// Permission: loans:apply.

import { useEffect, useState } from 'react';
import { useAuth } from '../../../auth/AuthContext';
import {
  createLoanApplication,
  listLoanProducts,
  listCounterparties,
  type LoanProduct,
  type Counterparty,
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

  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

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
        guarantors: [],
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
