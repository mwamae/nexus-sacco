// Loans — application list / loan list / detail / new-application wizard.
//
// Single-page experience routed by URL:
//   /loans                      → tabbed list (Applications | Loans)
//   /loans/applications/<id>    → application detail (score, actions)
//   /loans/<id>                 → loan detail (schedule, transactions)

import { useEffect, useState, type ReactNode } from 'react';
import {
  acceptLoanOffer,
  approveLoanApplication,
  createLoanApplication,
  declineLoanApplication,
  disburseLoan,
  extractError,
  getDepositAccountsByMember,
  getLoan,
  getLoanApplication,
  getLoanArrearsSummary,
  getLoanPayoff,
  listLoanApplications,
  listLoanProducts,
  listLoanPurposeCategories,
  listLoans,
  listMembers,
  listRestructurings,
  moratoriumLoan,
  recalcLoanDPD,
  recordTopupIntent,
  repayLoan,
  rescheduleLoan,
  rescoreLoanApplication,
  reverseLoanTxn,
  sendLoanOffer,
  settleLoan,
  settlementDiscountLoan,
  type LoanRestructuring,
  type ApiMember,
  type ArrearsSummary,
  type Loan,
  type LoanAppDetail,
  type LoanAppListItem,
  type LoanAppStatus,
  type LoanApplication,
  type LoanCollateralKind,
  type LoanDetail,
  type LoanEmploymentType,
  type LoanListItem,
  type LoanProduct,
  type LoanPurposeCategory,
  type LoanScoreFactor,
  type LoanScoreFlag,
  type LoanScoreResult,
  type LoanStatus,
  type MemberDepositItem,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

export default function LoansPage() {
  const path = window.location.pathname;
  const appMatch = /^\/loans\/applications\/([0-9a-f-]{36})/.exec(path);
  const loanMatch = /^\/loans\/([0-9a-f-]{36})/.exec(path);
  if (appMatch) return <AppDetail appId={appMatch[1]} />;
  if (loanMatch) return <LoanDetailView loanId={loanMatch[1]} />;
  return <ListsView />;
}

// ─────────── Lists view (apps + loans, tabbed) ───────────

function ListsView() {
  const { hasPermission } = useAuth();
  const canApply = hasPermission('loans:apply');
  const [tab, setTab] = useState<'apps' | 'loans'>('apps');
  const [openNew, setOpenNew] = useState(false);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Lending · Operations</div>
          <h1>Loans</h1>
          <div className="page-sub">Applications, scoring, approval, and active loan accounts.</div>
        </div>
        <div className="page-hd-actions">
          {canApply && (
            <button className="btn btn-sm btn-accent" onClick={() => setOpenNew(true)}>
              <Icon name="plus" size={12} /> New application
            </button>
          )}
        </div>
      </div>

      <ArrearsWidget />

      <div className="row" style={{ gap: 4, marginBottom: 14 }}>
        <button className="btn btn-sm" data-active={tab === 'apps' || undefined} onClick={() => setTab('apps')}>Applications</button>
        <button className="btn btn-sm" data-active={tab === 'loans' || undefined} onClick={() => setTab('loans')}>Active loans</button>
      </div>

      {tab === 'apps' ? <AppList /> : <LoanList />}

      {openNew && canApply && (
        <NewApplicationModal onClose={() => setOpenNew(false)} onCreated={(appId) => {
          window.location.href = `/loans/applications/${appId}`;
        }} />
      )}
    </div>
  );
}

// ─────────── Application list ───────────

function AppList() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [items, setItems] = useState<LoanAppListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [status, setStatus] = useState('');
  const [q, setQ] = useState('');

  async function reload() {
    setErr(null);
    try {
      const r = await listLoanApplications({ status: status || undefined, q: q || undefined, limit: 100 });
      setItems(r.items); setTotal(r.total);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status]);

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Applications</h3>
        <span className="card-sub">{total} total</span>
        <div className="card-hd-actions">
          <form onSubmit={(e) => { e.preventDefault(); void reload(); }} style={{ display: 'flex', gap: 4 }}>
            <input className="input" style={{ height: 26, fontSize: 12, width: 200 }} placeholder="Member / app #" value={q} onChange={(e) => setQ(e.target.value)} />
            <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
          </form>
          <select className="input" style={{ height: 26, fontSize: 12 }} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All statuses</option>
            {(['pending_scoring', 'pending_approval', 'approved', 'approved_with_conditions', 'declined', 'offer_sent', 'offer_accepted', 'disbursed', 'cancelled'] as LoanAppStatus[]).map((s) => (
              <option key={s} value={s}>{s.replace(/_/g, ' ')}</option>
            ))}
          </select>
        </div>
      </div>
      <div className="card-body flush">
        {err && <div style={{ padding: 12 }}><div className="alert alert-error">{err}</div></div>}
        {items.length === 0 && !err && <div className="empty">No applications.</div>}
        {items.length > 0 && (
          <table className="tbl">
            <thead>
              <tr>
                <th>App #</th>
                <th>Member</th>
                <th>Product</th>
                <th style={{ textAlign: 'right' }}>Requested</th>
                <th>Status</th>
                <th>Score</th>
                <th>Created</th>
                <th style={{ width: 1 }}></th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (
                <tr key={it.application.id}>
                  <td className="tiny-mono"><a className="tbl-link" href={`/loans/applications/${it.application.id}`}>{it.application.application_no}</a></td>
                  <td>
                    <div>{it.member_name}</div>
                    <div className="muted tiny">{it.member_no}</div>
                  </td>
                  <td>
                    <div>{it.product_name}</div>
                    <div className="muted tiny">{it.product_code}</div>
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(it.application.requested_amount)}</td>
                  <td><StatusBadge status={it.application.status} /></td>
                  <td>{it.application.credit_score != null ? <Badge tone={scoreBandTone(it.application.risk_band)}>{it.application.credit_score} · {it.application.risk_band}</Badge> : '—'}</td>
                  <td className="tiny-mono">{it.application.created_at.slice(0, 10)}</td>
                  <td><a className="btn btn-sm" href={`/loans/applications/${it.application.id}`}><Icon name="eye" size={12} /></a></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function scoreBandTone(band?: string): 'pos' | 'neg' | 'accent' | 'warn' {
  switch (band) {
    case 'A': return 'pos';
    case 'B': return 'accent';
    case 'C': return 'warn';
    case 'D': return 'neg';
    default: return 'accent';
  }
}

// ─────────── Loan list ───────────

function LoanList() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [items, setItems] = useState<LoanListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [status, setStatus] = useState('');

  async function reload() {
    setErr(null);
    try {
      const r = await listLoans({ status: status || undefined, limit: 100 });
      setItems(r.items); setTotal(r.total);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [status]);

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Loan accounts</h3>
        <span className="card-sub">{total} total</span>
        <div className="card-hd-actions">
          <select className="input" style={{ height: 26, fontSize: 12 }} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All statuses</option>
            {(['pending_disbursement', 'active', 'in_arrears', 'defaulted', 'restructured', 'settled', 'written_off', 'closed'] as LoanStatus[]).map((s) => (
              <option key={s} value={s}>{s.replace(/_/g, ' ')}</option>
            ))}
          </select>
        </div>
      </div>
      <div className="card-body flush">
        {err && <div style={{ padding: 12 }}><div className="alert alert-error">{err}</div></div>}
        {items.length === 0 && !err && <div className="empty">No loans yet. Approved applications appear here after disbursement.</div>}
        {items.length > 0 && (
          <table className="tbl">
            <thead>
              <tr>
                <th>Loan #</th>
                <th>Member</th>
                <th>Product</th>
                <th>Status</th>
                <th style={{ textAlign: 'right' }}>Principal</th>
                <th style={{ textAlign: 'right' }}>Balance</th>
                <th>Next due</th>
                <th>DPD</th>
                <th style={{ width: 1 }}></th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (
                <tr key={it.loan.id}>
                  <td className="tiny-mono"><a className="tbl-link" href={`/loans/${it.loan.id}`}>{it.loan.loan_no}</a></td>
                  <td>{it.member_name}<div className="muted tiny">{it.member_no}</div></td>
                  <td>{it.product_name}</td>
                  <td><StatusBadge status={it.loan.status} /></td>
                  <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(it.loan.principal)}</td>
                  <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(it.loan.principal_balance)}</td>
                  <td className="tiny-mono">{it.loan.next_installment_due_at ? it.loan.next_installment_due_at.slice(0, 10) : '—'}</td>
                  <td>{it.loan.days_past_due > 0 ? <Badge tone="neg">{it.loan.days_past_due}d</Badge> : <Badge tone="pos">0</Badge>}</td>
                  <td><a className="btn btn-sm" href={`/loans/${it.loan.id}`}><Icon name="eye" size={12} /></a></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────── New application wizard ───────────

function NewApplicationModal({ onClose, onCreated }: { onClose: () => void; onCreated: (appId: string) => void }) {
  const [products, setProducts] = useState<LoanProduct[]>([]);
  const [purposes, setPurposes] = useState<LoanPurposeCategory[]>([]);
  const [members, setMembers] = useState<ApiMember[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [memberID, setMemberID] = useState('');
  const [productID, setProductID] = useState('');
  const product = products.find((p) => p.id === productID);
  const [amount, setAmount] = useState('');
  const [term, setTerm] = useState<number>(12);
  const [purposeID, setPurposeID] = useState('');
  const [purposeNote, setPurposeNote] = useState('');
  const [employment, setEmployment] = useState<LoanEmploymentType>('salaried');
  const [employer, setEmployer] = useState('');
  const [income, setIncome] = useState('');
  const [otherIncome, setOtherIncome] = useState('0');
  const [expenses, setExpenses] = useState('');
  const [obligations, setObligations] = useState('0');

  const [guarantors, setGuarantors] = useState<{ member_id: string; amount: string }[]>([]);
  const [collateral, setCollateral] = useState<{ kind: LoanCollateralKind; description: string; estimated_value: string }[]>([]);

  useEffect(() => {
    Promise.all([
      listLoanProducts(false),
      listLoanPurposeCategories(false),
      listMembers({ limit: 200 }),
    ]).then(([p, pc, m]) => {
      setProducts(p); setPurposes(pc); setMembers(m.members);
      if (p.length > 0) setProductID(p[0].id);
    }).catch((e) => setErr(extractError(e)));
  }, []);

  useEffect(() => {
    if (product?.default_term_months) setTerm(product.default_term_months);
  }, [product]);

  async function submit() {
    if (!memberID || !product || !amount || !income || !expenses) {
      setErr('Pick a member + product, and fill amount + income + expenses.');
      return;
    }
    setBusy(true); setErr(null);
    try {
      const r = await createLoanApplication({
        member_id: memberID,
        product_id: product.id,
        requested_amount: amount,
        requested_term_months: term,
        purpose_category_id: purposeID || undefined,
        purpose_note: purposeNote || undefined,
        employment_type: employment,
        employer_name: employer || undefined,
        monthly_net_income: income,
        other_income: otherIncome || '0',
        monthly_expenses: expenses,
        monthly_existing_obligations: obligations || '0',
        guarantors: guarantors.map((g) => ({ member_id: g.member_id, amount_guaranteed: g.amount })),
        collateral: collateral,
      });
      onCreated(r.application.id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <ModalShell title="New loan application" busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Submit + score"
      disabled={!memberID || !product || !amount || !income || !expenses} width={700}>
      {err && <div className="alert alert-error">{err}</div>}
      <Section title="Member & product">
        <div className="grid-2">
          <Field label="Member">
            <select className="input" value={memberID} onChange={(e) => setMemberID(e.target.value)}>
              <option value="">— pick a member —</option>
              {members.map((m) => <option key={m.id} value={m.id}>{m.full_name} · {m.member_no}</option>)}
            </select>
          </Field>
          <Field label="Product">
            <select className="input" value={productID} onChange={(e) => setProductID(e.target.value)}>
              {products.map((p) => <option key={p.id} value={p.id}>{p.code} · {p.name}</option>)}
            </select>
          </Field>
        </div>
        {product && (
          <p className="muted tiny" style={{ margin: '0 0 10px' }}>
            Min {product.min_amount} – max {product.max_amount} · {product.min_term_months}–{product.max_term_months} months · {product.interest_rate_pct}% {product.interest_method.replace('_', ' ')}
            {product.min_guarantors > 0 && <> · requires {product.min_guarantors} guarantor{product.min_guarantors === 1 ? '' : 's'}</>}
          </p>
        )}
      </Section>

      <Section title="Loan request">
        <div className="grid-3">
          <Field label="Amount"><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="0" /></Field>
          <Field label="Term (months)"><input className="input mono" type="number" min={1} value={term} onChange={(e) => setTerm(parseInt(e.target.value, 10) || 0)} /></Field>
          <Field label="Purpose category">
            <select className="input" value={purposeID} onChange={(e) => setPurposeID(e.target.value)}>
              <option value="">— optional —</option>
              {purposes.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </Field>
        </div>
        <Field label="Purpose note (free text)"><input className="input" value={purposeNote} onChange={(e) => setPurposeNote(e.target.value)} /></Field>
      </Section>

      <Section title="Income">
        <div className="grid-3">
          <Field label="Employment type">
            <select className="input" value={employment} onChange={(e) => setEmployment(e.target.value as LoanEmploymentType)}>
              <option value="salaried">Salaried</option>
              <option value="self_employed">Self-employed</option>
              <option value="business_owner">Business owner</option>
              <option value="retired">Retired</option>
              <option value="student">Student</option>
              <option value="other">Other</option>
            </select>
          </Field>
          <Field label="Employer (optional)"><input className="input" value={employer} onChange={(e) => setEmployer(e.target.value)} /></Field>
          <Field label="Monthly net income"><input className="input mono" value={income} onChange={(e) => setIncome(e.target.value)} /></Field>
          <Field label="Other monthly income"><input className="input mono" value={otherIncome} onChange={(e) => setOtherIncome(e.target.value)} /></Field>
          <Field label="Monthly expenses"><input className="input mono" value={expenses} onChange={(e) => setExpenses(e.target.value)} /></Field>
          <Field label="Existing monthly obligations"><input className="input mono" value={obligations} onChange={(e) => setObligations(e.target.value)} /></Field>
        </div>
      </Section>

      <Section title={`Guarantors (${guarantors.length} added)`}>
        {guarantors.map((g, i) => (
          <div key={i} className="grid-3" style={{ alignItems: 'flex-end' }}>
            <Field label="Guarantor member">
              <select className="input" value={g.member_id} onChange={(e) => {
                const next = [...guarantors]; next[i].member_id = e.target.value; setGuarantors(next);
              }}>
                <option value="">— pick —</option>
                {members.filter((m) => m.id !== memberID).map((m) => <option key={m.id} value={m.id}>{m.full_name}</option>)}
              </select>
            </Field>
            <Field label="Amount guaranteed">
              <input className="input mono" value={g.amount} onChange={(e) => {
                const next = [...guarantors]; next[i].amount = e.target.value; setGuarantors(next);
              }} />
            </Field>
            <button className="btn btn-sm" style={{ color: 'var(--neg)', marginBottom: 10 }} onClick={() => setGuarantors(guarantors.filter((_, j) => j !== i))}>Remove</button>
          </div>
        ))}
        <button className="btn btn-sm" onClick={() => setGuarantors([...guarantors, { member_id: '', amount: '0' }])}>+ Add guarantor</button>
      </Section>
    </ModalShell>
  );
}

// ─────────── Application detail ───────────

function AppDetail({ appId }: { appId: string }) {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [d, setD] = useState<LoanAppDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const canApprove = hasPermission('loans:approve');
  const canOffer = hasPermission('loans:offer');
  const canAssess = hasPermission('loans:assess');

  async function load() {
    setErr(null);
    try { setD(await getLoanApplication(appId)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [appId]);

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!d) return <div className="page"><div className="empty">Loading…</div></div>;

  const a = d.application;
  // Parse stored scoring details / flags.
  let factors: LoanScoreFactor[] = [];
  let flags: LoanScoreFlag[] = [];
  try { if (a.scoring_details) factors = JSON.parse(a.scoring_details as string); } catch { /* ignore */ }
  try { if (a.scoring_flags) flags = JSON.parse(a.scoring_flags as string); } catch { /* ignore */ }

  async function run(label: string, fn: () => Promise<unknown>) {
    setBusy(label);
    try { await fn(); await load(); }
    catch (e) { alert(extractError(e)); }
    finally { setBusy(null); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/loans" style={{ color: 'inherit' }}>Loans · Applications</a> · {a.application_no}</div>
          <h1>Application {a.application_no}</h1>
          <div className="page-sub">Requested {currency} {fmt(a.requested_amount)} over {a.requested_term_months} months</div>
        </div>
        <div className="page-hd-actions"><StatusBadge status={a.status} /></div>
      </div>

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Credit score" value={a.credit_score != null ? String(a.credit_score) : '—'} sub={a.risk_band ? `Band ${a.risk_band}` : undefined} tone={scoreTone(a.credit_score)} />
        <KPI label="Affordability" value={a.affordability_pass == null ? '—' : a.affordability_pass ? 'PASS' : 'FAIL'} tone={a.affordability_pass ? 'pos' : a.affordability_pass === false ? 'neg' : undefined} />
        <KPI label="DTI" value={a.dti_ratio ? `${a.dti_ratio}%` : '—'} sub="(installments / income)" />
        <KPI label="Disposable income" value={a.net_disposable_income ? `${currency} ${fmt(a.net_disposable_income)}` : '—'} />
      </div>

      {factors.length > 0 && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd">
            <h3>Score breakdown</h3>
            <span className="card-sub">Overall {a.credit_score}/100 · {a.risk_band}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Factor</th><th style={{ textAlign: 'right' }}>Score</th><th style={{ textAlign: 'right' }}>Weight</th><th>Note</th></tr></thead>
              <tbody>
                {factors.map((f, i) => (
                  <tr key={i}>
                    <td>{f.name}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{f.score}/100</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{f.weight}%</td>
                    <td className="tiny muted">{f.note}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {flags.length > 0 && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd"><h3>Flags</h3></div>
          <div className="card-body">
            {flags.map((f, i) => (
              <div key={i} className={`alert ${f.severity === 'hard_block' ? 'alert-error' : 'alert-warn'}`} style={{ marginBottom: 4 }}>
                <strong>{f.severity.replace('_', ' ')}</strong> · {f.code} — {f.message}
              </div>
            ))}
          </div>
        </div>
      )}

{d.schedule && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd">
            <h3>Projected repayment schedule</h3>
            <span className="card-sub">
              Snapshot at {d.schedule.generated_at?.slice(0, 10)} ·
              {' '}{d.schedule.repayment_method.replace('_', ' ')} ·
              {' '}{d.schedule.interest_rate_pct}% p.a.
            </span>
          </div>
          <div className="card-body">
            <div className="grid-4" style={{ marginBottom: 12 }}>
              <KV label="Principal" value={`${currency} ${fmt(d.schedule.principal)}`} />
              <KV label="Installment" value={`${currency} ${fmt(d.schedule.installment)} / month`} />
              <KV label="Total interest" value={`${currency} ${fmt(d.schedule.total_interest)}`} />
              <KV label="Total payable" value={`${currency} ${fmt(d.schedule.total_payable)}`} />
            </div>
            <table className="tbl">
              <thead>
                <tr>
                  <th>#</th>
                  <th>Due date</th>
                  <th style={{ textAlign: 'right' }}>Principal</th>
                  <th style={{ textAlign: 'right' }}>Interest</th>
                  <th style={{ textAlign: 'right' }}>Total due</th>
                  <th style={{ textAlign: 'right' }}>Balance after</th>
                </tr>
              </thead>
              <tbody>
                {d.schedule.rows.map((row) => (
                  <tr key={row.installment_no}>
                    <td className="mono">{row.installment_no}</td>
                    <td className="mono">{row.due_date.slice(0, 10)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmt(row.principal_due)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmt(row.interest_due)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}><strong>{fmt(row.total_due)}</strong></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmt(row.outstanding_after)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd"><h3>Recommended terms</h3></div>
        <div className="card-body">
          <div className="grid-3">
            <KV label="Multiplier ceiling" value={a.computed_max_amount ? `${currency} ${fmt(a.computed_max_amount)}` : '—'} />
            <KV label="Affordability ceiling" value={a.computed_max_installment ? `${currency} ${fmt(a.computed_max_installment)} / month` : '—'} />
            <KV label="Recommended" value={a.recommended_amount ? `${currency} ${fmt(a.recommended_amount)} over ${a.recommended_term_months} mo` : '—'} />
          </div>
        </div>
      </div>

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd"><h3>Approval / disbursement actions</h3></div>
        <div className="card-body">
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {canAssess && <button className="btn btn-sm" disabled={!!busy} onClick={() => run('rescore', () => rescoreLoanApplication(appId))}>Re-score</button>}
            {canApprove && a.status === 'pending_approval' && (
              <>
                <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('approve', () => approveLoanApplication(appId, {}))}>Approve as applied</button>
                <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={!!busy} onClick={() => run('decline', async () => {
                  const reason = prompt('Decline reason?'); if (!reason) return;
                  await declineLoanApplication(appId, 'underwriter', reason);
                })}>Decline</button>
              </>
            )}
            {canOffer && (a.status === 'approved' || a.status === 'approved_with_conditions') && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('offer', () => sendLoanOffer(appId, {}))}>Send offer</button>
            )}
            {canOffer && a.status === 'offer_sent' && (
              <button className="btn btn-sm btn-accent" disabled={!!busy} onClick={() => run('accept', async () => {
                if (!confirm('Confirm acceptance on behalf of the member?')) return;
                const r = await acceptLoanOffer(appId);
                window.location.href = `/loans/${r.loan.id}`;
              })}>Accept offer (create loan)</button>
            )}
          </div>
          {a.approval_conditions && (
            <p className="muted tiny" style={{ marginTop: 8 }}><strong>Conditions:</strong> {a.approval_conditions}</p>
          )}
          {a.decline_reason && (
            <p className="muted tiny" style={{ marginTop: 8 }}><strong>Declined ({a.decline_category}):</strong> {a.decline_reason}</p>
          )}
        </div>
      </div>

      {d.guarantees.length > 0 && (
        <div className="card">
          <div className="card-hd"><h3>Guarantors</h3></div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Member</th><th>Amount</th><th>Status</th><th>Requested</th></tr></thead>
              <tbody>
                {d.guarantees.map((g: any) => (
                  <tr key={g.id}>
                    <td className="tiny-mono"><a className="tbl-link" href={`/members/${g.guarantor_member_id}`}>{g.guarantor_member_id.slice(0, 8)}…</a></td>
                    <td className="mono">{currency} {fmt(g.amount_guaranteed)}</td>
                    <td><Badge tone={g.status === 'accepted' ? 'pos' : g.status === 'declined' ? 'neg' : 'warn'}>{g.status.replace('_', ' ')}</Badge></td>
                    <td className="tiny-mono">{g.requested_at.slice(0, 10)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}

function scoreTone(s?: number): 'pos' | 'neg' | 'warn' | undefined {
  if (s == null) return undefined;
  if (s >= 80) return 'pos';
  if (s >= 65) return undefined;
  if (s >= 50) return 'warn';
  return 'neg';
}

// ─────────── Loan detail ───────────

function LoanDetailView({ loanId }: { loanId: string }) {
  const { tenant, hasPermission } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [d, setD] = useState<LoanDetail | null>(null);
  const [accounts, setAccounts] = useState<MemberDepositItem[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const canDisburse = hasPermission('loans:disburse');
  const canTransact = hasPermission('savings:transact');
  const canApprove = hasPermission('savings:approve');
  const canReverse = hasPermission('loans:reverse');
  const canRestructure = hasPermission('loans:restructure');
  const [modal, setModal] = useState<null | 'repay' | 'settle' | 'reschedule' | 'moratorium' | 'settlement_discount' | 'topup'>(null);
  const [restructurings, setRestructurings] = useState<LoanRestructuring[]>([]);

  async function load() {
    setErr(null);
    try {
      const v = await getLoan(loanId);
      setD(v);
      const accts = await getDepositAccountsByMember(v.loan.member_id);
      setAccounts(accts);
      const rest = await listRestructurings(loanId);
      setRestructurings(rest);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [loanId]);

  if (err) return <div className="page"><div className="alert alert-error">{err}</div></div>;
  if (!d) return <div className="page"><div className="empty">Loading…</div></div>;

  const l = d.loan;
  const totals = d.schedule.reduce(
    (acc, r) => ({ p: acc.p + parseFloat(r.principal_due), i: acc.i + parseFloat(r.interest_due), t: acc.t + parseFloat(r.total_due) }),
    { p: 0, i: 0, t: 0 }
  );

  async function disburse() {
    if (l.status !== 'pending_disbursement') return;
    const ord = accounts.find((a) => a.product.product_type === 'ordinary');
    if (!ord) { alert('Member has no ordinary savings account to disburse into.'); return; }
    if (!confirm(`Disburse to ${ord.account.account_no} (${ord.product.name})?`)) return;
    setBusy(true);
    try {
      const r = await disburseLoan(loanId, { channel: 'internal', target_account_id: ord.account.id });
      if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
      await load();
    } catch (e) { alert(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow"><a href="/loans" style={{ color: 'inherit' }}>Loans</a> · {l.loan_no}</div>
          <h1>Loan {l.loan_no}</h1>
          <div className="page-sub">
            <a href={`/members/${l.member_id}`} className="tbl-link">{l.member_id.slice(0, 8)}…</a> ·
            {' '}{currency} {fmt(l.principal)} @ {l.interest_rate_pct}% {l.interest_method.replace('_', ' ')} · {l.term_months} months
          </div>
        </div>
        <div className="page-hd-actions">
          <StatusBadge status={l.status} />
          {l.days_past_due > 0 && (
            <Badge tone={CLASS_TONE[l.arrears_classification] ?? 'neg'}>
              DPD {l.days_past_due}d · {l.arrears_classification}
            </Badge>
          )}
          {canTransact && (l.status === 'active' || l.status === 'in_arrears' || l.status === 'restructured') && (
            <button className="btn btn-sm btn-accent" onClick={() => setModal('repay')}>Repay</button>
          )}
          {canApprove && (l.status === 'active' || l.status === 'in_arrears' || l.status === 'restructured') && (
            <button className="btn btn-sm" onClick={() => setModal('settle')}>Settle</button>
          )}
          {canTransact && (l.status === 'active' || l.status === 'in_arrears') && (
            <button className="btn btn-sm" disabled={busy} onClick={async () => {
              setBusy(true);
              try { await recalcLoanDPD(loanId); await load(); }
              catch (e) { alert(extractError(e)); }
              finally { setBusy(false); }
            }}>Recalc DPD</button>
          )}
          {canRestructure && (l.status === 'active' || l.status === 'in_arrears' || l.status === 'restructured') && (
            <>
              <button className="btn btn-sm" onClick={() => setModal('reschedule')}>Reschedule</button>
              <button className="btn btn-sm" onClick={() => setModal('moratorium')}>Moratorium</button>
              <button className="btn btn-sm" onClick={() => setModal('settlement_discount')}>Settlement discount</button>
              <button className="btn btn-sm" onClick={() => setModal('topup')}>Top-up intent</button>
            </>
          )}
        </div>
      </div>

      <div className="grid-4" style={{ marginBottom: 14 }}>
        <KPI label="Principal balance" value={`${currency} ${fmt(l.principal_balance)}`} />
        <KPI label="Interest balance" value={`${currency} ${fmt(l.interest_balance)}`} />
        <KPI
          label="Days past due"
          value={String(l.days_past_due)}
          sub={l.arrears_classification}
          tone={l.days_past_due > 0 ? 'neg' : 'pos'}
        />
        <KPI label="Next due" value={l.next_installment_due_at ? l.next_installment_due_at.slice(0, 10) : '—'} sub={l.next_installment_amount ? `${currency} ${fmt(l.next_installment_amount)}` : undefined} />
      </div>

      {canDisburse && l.status === 'pending_disbursement' && (
        <div className="card" style={{ marginBottom: 14 }}>
          <div className="card-hd"><h3>Disbursement</h3><span className="card-sub">Awaiting authorisation</span></div>
          <div className="card-body">
            <div className="alert alert-warn">
              Disbursing credits the member's ordinary savings account with the net amount (after upfront fees) and activates the amortisation schedule.
            </div>
            <button className="btn btn-sm btn-accent" disabled={busy} onClick={() => void disburse()}>
              {busy ? 'Disbursing…' : 'Disburse to ordinary savings'}
            </button>
          </div>
        </div>
      )}

      <div className="card" style={{ marginBottom: 14 }}>
        <div className="card-hd">
          <h3>Repayment schedule</h3>
          <span className="card-sub">{d.schedule.length} installments · total {currency} {totals.t.toFixed(2)} (P {totals.p.toFixed(2)} + I {totals.i.toFixed(2)})</span>
        </div>
        <div className="card-body flush">
          {d.schedule.length === 0 ? (
            <div className="empty">No schedule yet — generated on disbursement.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>#</th><th>Due</th>
                  <th style={{ textAlign: 'right' }}>Principal</th>
                  <th style={{ textAlign: 'right' }}>Interest</th>
                  <th style={{ textAlign: 'right' }}>Total</th>
                  <th style={{ textAlign: 'right' }}>Outstanding after</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {d.schedule.map((row) => (
                  <tr key={row.id}>
                    <td className="mono">{row.installment_no}</td>
                    <td className="tiny-mono">{row.due_date.slice(0, 10)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(row.principal_due)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(row.interest_due)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}><strong>{currency} {fmt(row.total_due)}</strong></td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(row.outstanding_after)}</td>
                    <td><Badge tone={row.status === 'paid' ? 'pos' : row.status === 'overdue' ? 'neg' : 'neutral'}>{row.status}</Badge></td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-hd"><h3>Transactions</h3><span className="card-sub">{d.transactions.length}</span></div>
        <div className="card-body flush">
          {d.transactions.length === 0 ? <div className="empty">No transactions yet.</div> : (
            <table className="tbl">
              <thead><tr><th>Txn</th><th>Type</th><th style={{ textAlign: 'right' }}>Amount</th><th>Narration</th><th>Posted</th>{canReverse && <th></th>}</tr></thead>
              <tbody>
                {d.transactions.map((t) => {
                  const isReversedRepayment = t.txn_type === 'repayment';
                  const alreadyReversed = d.transactions.some((x) => x.txn_type === 'reversal' && x.narration?.includes(t.txn_no));
                  return (
                    <tr key={t.id} style={{ textDecoration: alreadyReversed ? 'line-through' : undefined }}>
                      <td className="tiny-mono">{t.txn_no}</td>
                      <td><Badge tone={txnTypeTone(t.txn_type)}>{t.txn_type.replace('_', ' ')}</Badge></td>
                      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmt(t.amount)}</td>
                      <td className="tiny">{t.narration}</td>
                      <td className="tiny-mono">{t.posted_at.slice(0, 16).replace('T', ' ')}</td>
                      {canReverse && (
                        <td>
                          {isReversedRepayment && !alreadyReversed && (
                            <button className="btn btn-sm" style={{ color: 'var(--neg)' }} disabled={busy} onClick={async () => {
                              const reason = prompt(`Reverse ${t.txn_no}? Reason:`); if (!reason) return;
                              setBusy(true);
                              try {
                                const r = await reverseLoanTxn(t.id, reason);
                                if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
                                await load();
                              }
                              catch (e) { alert(extractError(e)); }
                              finally { setBusy(false); }
                            }}>Reverse</button>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {restructurings.length > 0 && (
        <div className="card" style={{ marginTop: 14 }}>
          <div className="card-hd">
            <h3>Restructuring history</h3>
            <span className="card-sub">{restructurings.length} event{restructurings.length === 1 ? '' : 's'}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>When</th><th>Kind</th><th>Reason</th><th>Detail</th></tr></thead>
              <tbody>
                {restructurings.map((r) => (
                  <tr key={r.id}>
                    <td className="tiny-mono">{r.created_at.slice(0, 16).replace('T', ' ')}</td>
                    <td><Badge tone="accent">{r.kind.replace('_', ' ')}</Badge></td>
                    <td className="tiny">{r.reason}</td>
                    <td className="tiny muted">
                      {r.kind === 'reschedule' && r.new_term_months && <>new term: {r.new_term_months}m</>}
                      {r.kind === 'moratorium' && r.moratorium_months && <>+{r.moratorium_months}m holiday {r.moratorium_suspend_interest ? '(no interest)' : ''}</>}
                      {r.kind === 'settlement_discount' && r.discount_amount && <>discount: {currency} {fmt(r.discount_amount)}</>}
                      {r.kind === 'topup' && r.topup_amount && <>topup: {currency} {fmt(r.topup_amount)}</>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {modal === 'repay' && (
        <RepayModal loan={l} accounts={accounts}
          onClose={() => setModal(null)}
          onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'settle' && (
        <SettleModal loan={l} accounts={accounts}
          onClose={() => setModal(null)}
          onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'reschedule' && (
        <RescheduleModal loan={l} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'moratorium' && (
        <MoratoriumModal loan={l} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'settlement_discount' && (
        <SettlementDiscountModal loan={l} currency={currency} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
      {modal === 'topup' && (
        <TopupIntentModal loan={l} onClose={() => setModal(null)} onPosted={async () => { setModal(null); await load(); }} />
      )}
    </div>
  );
}

// ─────────── Restructuring modals ───────────

function RescheduleModal({ loan, onClose, onPosted }: { loan: Loan; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [term, setTerm] = useState<number>(loan.term_months + 6);
  const [rate, setRate] = useState<string>('');
  const [firstDue, setFirstDue] = useState<string>(new Date(Date.now() + 30 * 86400e3).toISOString().slice(0, 10));
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Reschedule loan ${loan.loan_no}`} busy={busy} onClose={onClose}
      submitLabel="Apply reschedule" disabled={term <= 0 || !reason.trim()}
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          const r = await rescheduleLoan(loan.id, {
            new_term_months: term,
            new_interest_rate_pct: rate || undefined,
            new_first_due_date: firstDue,
            reason,
          });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onPosted();
        } catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        Re-amortises the remaining outstanding principal over a new term. Existing unpaid schedule rows are cancelled and replaced.
      </div>
      <Field label="New term (months)"><input className="input mono" type="number" min={1} value={term} onChange={(e) => setTerm(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="New interest rate (% p.a., optional)"><input className="input mono" value={rate} onChange={(e) => setRate(e.target.value)} placeholder={`current ${loan.interest_rate_pct}%`} /></Field>
      <Field label="New first due date"><input className="input mono" type="date" value={firstDue} onChange={(e) => setFirstDue(e.target.value)} /></Field>
      <Field label="Reason (audit)"><textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
    </ModalShell>
  );
}

function MoratoriumModal({ loan, onClose, onPosted }: { loan: Loan; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [months, setMonths] = useState<number>(2);
  const [suspendInterest, setSuspendInterest] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Apply moratorium · ${loan.loan_no}`} busy={busy} onClose={onClose}
      submitLabel="Apply moratorium" disabled={months <= 0 || !reason.trim()}
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          const r = await moratoriumLoan(loan.id, { moratorium_months: months, suspend_interest: suspendInterest, reason });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onPosted();
        } catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        Defers every unpaid installment forward by the chosen number of months. Loan status becomes <strong>restructured</strong>.
      </div>
      <Field label="Months to defer"><input className="input mono" type="number" min={1} max={12} value={months} onChange={(e) => setMonths(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Interest behaviour">
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={suspendInterest} onChange={(e) => setSuspendInterest(e.target.checked)} />
          <span>{suspendInterest ? 'Suspend interest accrual during the moratorium' : 'Interest continues to accrue during the moratorium'}</span>
        </label>
      </Field>
      <Field label="Reason (audit)"><textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
    </ModalShell>
  );
}

function SettlementDiscountModal({ loan, currency, onClose, onPosted }: { loan: Loan; currency: string; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [discount, setDiscount] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Settlement discount · ${loan.loan_no}`} busy={busy} onClose={onClose}
      submitLabel="Apply discount + settle" disabled={!discount || parseFloat(discount) <= 0 || !reason.trim()}
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          const r = await settlementDiscountLoan(loan.id, { discount_amount: discount, reason });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onPosted();
        } catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        Writes off the discount amount against the loan, marks all balances zero, and sets status to <strong>settled</strong>. Requires board / committee authorisation.
      </div>
      <Field label="Current outstanding">
        <input className="input mono" value={`${currency} ${fmt((parseFloat(loan.principal_balance) + parseFloat(loan.interest_balance) + parseFloat(loan.fees_balance) + parseFloat(loan.penalty_balance)).toFixed(2))}`} disabled />
      </Field>
      <Field label={`Discount amount being written off (${currency})`}>
        <input className="input mono" value={discount} onChange={(e) => setDiscount(e.target.value)} placeholder="e.g. 1500" />
      </Field>
      <Field label="Reason / authorisation"><textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Board resolution reference + context" /></Field>
    </ModalShell>
  );
}

function TopupIntentModal({ loan, onClose, onPosted }: { loan: Loan; onClose: () => void; onPosted: () => Promise<void> | void }) {
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Top-up intent · ${loan.loan_no}`} busy={busy} onClose={onClose}
      submitLabel="Record intent" disabled={!amount || parseFloat(amount) <= 0 || !reason.trim()}
      onSubmit={async () => {
        setBusy(true); setErr(null);
        try {
          await recordTopupIntent(loan.id, amount, reason);
          await onPosted();
        } catch (e) { setErr(extractError(e)); }
        finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Records the top-up intent in the restructuring audit log. To create the new combined loan, go through the standard <a href="/loans" className="tbl-link">new application</a> flow and select the same member + a compatible product.
      </p>
      <Field label="Requested top-up amount"><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
      <Field label="Reason"><textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
    </ModalShell>
  );
}

function txnTypeTone(t: string): 'pos' | 'neg' | 'warn' | 'accent' | 'neutral' {
  switch (t) {
    case 'disbursement': return 'pos';
    case 'repayment': return 'pos';
    case 'reversal': return 'warn';
    case 'interest_accrual': return 'accent';
    case 'fee_charge': case 'penalty_charge': return 'neg';
    default: return 'neutral';
  }
}

// ─────────── Atoms ───────────

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color, fontSize: 18 }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
    </div>
  );
}

function KV({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div>{value}</div>
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
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

// ─────────── Arrears widget (Phase 6d) ───────────

const CLASS_TONE: Record<string, 'pos' | 'neg' | 'warn' | 'accent' | 'neutral'> = {
  performing: 'pos',
  watch: 'accent',
  substandard: 'warn',
  doubtful: 'neg',
  loss: 'neg',
};

function ArrearsWidget() {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [s, setS] = useState<ArrearsSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    getLoanArrearsSummary().then(setS).catch((e) => setErr(extractError(e)));
  }, []);
  if (err) {
    return <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>;
  }
  if (!s) return null;

  return (
    <div className="card" style={{ marginBottom: 14 }}>
      <div className="card-hd">
        <h3>Arrears + classification</h3>
        <span className="card-sub">{s.total_loans} active loans · NPL {s.npl_ratio_pct}%</span>
      </div>
      <div className="card-body">
        <div className="row" style={{ flexWrap: 'wrap', gap: 8 }}>
          {(s.bands || []).map((b) => (
            <a
              key={b.classification}
              href={`/loans?status=in_arrears`}
              style={{
                display: 'inline-flex', alignItems: 'center', gap: 6,
                padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 'var(--r-md)',
                background: 'var(--surface)', textDecoration: 'none',
              }}
            >
              <Badge tone={CLASS_TONE[b.classification] ?? 'neutral'}>{b.classification.replace('_', ' ')}</Badge>
              <strong className="mono">{b.loan_count}</strong>
              <span className="muted tiny">· {currency} {fmt(b.total_outstanding)}</span>
            </a>
          ))}
        </div>
        <p className="muted tiny" style={{ marginTop: 10 }}>
          Total outstanding {currency} {fmt(s.total_outstanding)} · NPL {currency} {fmt(s.npl_outstanding)} across {s.npl_loan_count} loan{s.npl_loan_count === 1 ? '' : 's'}.
        </p>
      </div>
    </div>
  );
}

// ─────────── Repay modal ───────────

function RepayModal({ loan, accounts, onClose, onPosted }: {
  loan: Loan;
  accounts: MemberDepositItem[];
  onClose: () => void;
  onPosted: () => Promise<void> | void;
}) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [amount, setAmount] = useState('');
  const [channel, setChannel] = useState('mpesa');
  const [ref, setRef] = useState('');
  const [narration, setNarration] = useState('');
  const [debitAcct, setDebitAcct] = useState<string>('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    setErr(null); setBusy(true);
    try {
      const r = await repayLoan(loan.id, {
        amount, channel,
        channel_ref: ref || undefined,
        narration: narration || undefined,
        debit_savings_account_id: channel === 'auto_savings' ? debitAcct : undefined,
      });
      if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
      await onPosted();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title={`Record repayment · ${loan.loan_no}`} busy={busy} onClose={onClose} onSubmit={submit} submitLabel="Post repayment"
      disabled={!amount || parseFloat(amount) <= 0 || !channel || (channel === 'auto_savings' && !debitAcct)}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>
        Outstanding: principal {currency} {fmt(loan.principal_balance)} · interest {currency} {fmt(loan.interest_balance)} · fees {currency} {fmt(loan.fees_balance)} · penalty {currency} {fmt(loan.penalty_balance)}
      </p>
      <Field label={`Amount (${currency})`}>
        <input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="0.00" />
      </Field>
      <Field label="Channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value)}>
          <option value="mpesa">M-Pesa Paybill</option>
          <option value="auto_savings">Auto-debit from savings</option>
          <option value="bank">Bank standing order</option>
          <option value="payroll">Payroll deduction</option>
          <option value="teller">Manual teller payment</option>
        </select>
      </Field>
      {channel === 'auto_savings' && (
        <Field label="Debit from savings account">
          <select className="input" value={debitAcct} onChange={(e) => setDebitAcct(e.target.value)}>
            <option value="">— pick —</option>
            {accounts.map((a) => (
              <option key={a.account.id} value={a.account.id}>
                {a.product.name} · {a.account.account_no} · {currency} {fmt(a.account.available_balance)} available
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field label="Channel reference (M-Pesa code, till receipt, etc.)">
        <input className="input mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="optional" />
      </Field>
      <Field label="Narration">
        <input className="input" value={narration} onChange={(e) => setNarration(e.target.value)} placeholder="optional" />
      </Field>
      <p className="muted tiny">
        Repayment allocates per the tenant's waterfall: penalty → interest → principal → fees. Any overpayment goes into suspense.
      </p>
    </ModalShell>
  );
}

// ─────────── Settle modal ───────────

function SettleModal({ loan, accounts, onClose, onPosted }: {
  loan: Loan;
  accounts: MemberDepositItem[];
  onClose: () => void;
  onPosted: () => Promise<void> | void;
}) {
  const { tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';
  const [payoff, setPayoff] = useState<string | null>(null);
  const [channel, setChannel] = useState('teller');
  const [ref, setRef] = useState('');
  const [debitAcct, setDebitAcct] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    getLoanPayoff(loan.id).then((r) => setPayoff(r.payoff)).catch((e) => setErr(extractError(e)));
  }, [loan.id]);

  async function submit() {
    setErr(null); setBusy(true);
    try {
      const r = await settleLoan(loan.id, {
        channel, channel_ref: ref || undefined,
        debit_savings_account_id: channel === 'auto_savings' ? debitAcct : undefined,
      });
      if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
      await onPosted();
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <ModalShell title={`Early settlement · ${loan.loan_no}`} busy={busy} onClose={onClose} onSubmit={submit} submitLabel={`Settle ${currency} ${fmt(payoff ?? '0')}`}
      disabled={!payoff || (channel === 'auto_savings' && !debitAcct)}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">
        Settlement pays the full outstanding payoff figure (after accruing any pending interest). The loan moves to <strong>settled</strong> on success — reverse only if needed.
      </div>
      <Field label="Payoff (computed)">
        <input className="input mono" value={payoff ? `${currency} ${fmt(payoff)}` : 'Loading…'} disabled />
      </Field>
      <Field label="Channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value)}>
          <option value="teller">Manual teller payment</option>
          <option value="mpesa">M-Pesa Paybill</option>
          <option value="bank">Bank transfer</option>
          <option value="auto_savings">Auto-debit from savings</option>
        </select>
      </Field>
      {channel === 'auto_savings' && (
        <Field label="Debit from savings account">
          <select className="input" value={debitAcct} onChange={(e) => setDebitAcct(e.target.value)}>
            <option value="">— pick —</option>
            {accounts.map((a) => (
              <option key={a.account.id} value={a.account.id}>
                {a.product.name} · {a.account.account_no} · {currency} {fmt(a.account.available_balance)}
              </option>
            ))}
          </select>
        </Field>
      )}
      <Field label="Channel reference"><input className="input mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="optional" /></Field>
    </ModalShell>
  );
}

function ModalShell({ title, busy, onClose, children, submitLabel, onSubmit, disabled, width }: {
  title: string; busy?: boolean; onClose: () => void;
  children: ReactNode; submitLabel: string; onSubmit: () => void | Promise<void>;
  disabled?: boolean; width?: number;
}) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: width ?? 560, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || disabled} onClick={() => void onSubmit()}>{busy ? 'Working…' : submitLabel}</button>
        </div>
      </div>
    </div>
  );
}
