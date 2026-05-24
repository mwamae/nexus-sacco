// Collection Desk — the single "cashier's counter" page (/collect).
//
// Four-step state machine driven from the URL nothing (in-memory only):
//   1. find — counterparty search + result card
//   2. build — receipt-builder right rail (typed line picker)
//   3. payment — channel + reference + amount + value date
//   4. confirm — receipt preview + post button
//
// The desk is gated on the teller having an open till session for
// cash. Non-cash channels auto-provision a per-channel virtual till
// server-side; the desk just picks the channel and supplies the ref.
//
// Backend contract: services/savings/internal/handler/collection_desk.go.

import { useEffect, useMemo, useState } from 'react';
import {
  createReceipt,
  extractError,
  getCurrentTillSession,
  getDepositAccountsByMember,
  getMemberLoanHistory,
  getOutstanding,
  listCounterparties,
  listFees,
  type ApiReceipt,
  type Counterparty,
  type CounterpartyOutstanding,
  type CreateReceiptLineInput,
  type CurrentTillSession,
  type FeeCatalogEntry,
  type Loan,
  type MemberDepositItem,
  type ReceiptChannel,
  type ReceiptLineKind,
} from '../api/client';
import { useDocumentTitle } from '../lib/useDocumentTitle';

type LineDraft = {
  // ephemeral client id so React can key the rows
  rowKey: string;
  kind: ReceiptLineKind;
  amount: string;
  target_account_id?: string;
  fee_code?: string;
  narration?: string;
};

type Step = 'find' | 'build' | 'payment' | 'confirm';

const CHANNELS: { value: ReceiptChannel; label: string }[] = [
  { value: 'cash', label: 'Cash' },
  { value: 'mpesa', label: 'M-Pesa' },
  { value: 'airtel_money', label: 'Airtel Money' },
  { value: 'bank_transfer', label: 'Bank Transfer' },
  { value: 'cheque', label: 'Cheque' },
  { value: 'standing_order', label: 'Standing Order' },
];

const LINE_KIND_LABELS: Record<ReceiptLineKind, string> = {
  savings_deposit: 'Savings deposit',
  share_purchase: 'Share purchase',
  loan_repayment: 'Loan repayment',
  fee: 'Fee payment',
  welfare: 'Welfare contribution',
};

function rowKey() {
  return Math.random().toString(36).slice(2);
}

export default function CollectionDesk() {
  // Step 0 — load till session + fee catalog up front. Cash is blocked
  // without an open till; the fee catalog drives the line picker so
  // every "fee" line has a known code + GL account.
  const [till, setTill] = useState<CurrentTillSession | null>(null);
  const [tillErr, setTillErr] = useState<string | null>(null);
  const [fees, setFees] = useState<FeeCatalogEntry[]>([]);
  useDocumentTitle('Collection desk');
  useEffect(() => {
    getCurrentTillSession()
      .then((t) => setTill(t))
      .catch((e) => setTillErr(extractError(e)));
    listFees(false)
      .then((f) => setFees(f))
      .catch(() => setFees([])); // catalog absence shouldn't block the desk
  }, []);

  const [step, setStep] = useState<Step>('find');
  const [cp, setCp] = useState<Counterparty | null>(null);
  const [outstanding, setOutstanding] = useState<CounterpartyOutstanding | null>(null);
  // CP-scoped lookups for the line builder dropdowns. Fetched once
  // when the CP is picked; the builder consumes them as the source
  // of truth for the deposit-account / loan selects.
  const [cpDeposits, setCpDeposits] = useState<MemberDepositItem[]>([]);
  const [cpLoans, setCpLoans] = useState<Loan[]>([]);
  const [lines, setLines] = useState<LineDraft[]>([]);
  const [channel, setChannel] = useState<ReceiptChannel>('cash');
  const [channelRef, setChannelRef] = useState('');
  const [channelAmount, setChannelAmount] = useState('');
  const [valueDate, setValueDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [narration, setNarration] = useState('');
  const [posting, setPosting] = useState(false);
  const [postErr, setPostErr] = useState<string | null>(null);
  const [posted, setPosted] = useState<ApiReceipt | null>(null);

  const subtotal = useMemo(
    () =>
      lines.reduce((acc, l) => acc + (parseFloat(l.amount || '0') || 0), 0),
    [lines],
  );
  const channelAmountNum = parseFloat(channelAmount || '0') || 0;

  // Reset everything when "Start over" is clicked or a receipt posts.
  function reset() {
    setStep('find');
    setCp(null);
    setOutstanding(null);
    setCpDeposits([]);
    setCpLoans([]);
    setLines([]);
    setChannel('cash');
    setChannelRef('');
    setChannelAmount('');
    setNarration('');
    setPostErr(null);
    setPosted(null);
  }

  async function pickCp(c: Counterparty) {
    setCp(c);
    setStep('build');
    // Fan out the three CP-scoped lookups in parallel. Each one
    // degrades independently — outstanding is a nice-to-have for
    // the suggestions panel, and the deposit/loan lists drive the
    // builder dropdowns (the cashier can still pick share/fee
    // lines if those happen to fail).
    const [o, d, l] = await Promise.allSettled([
      getOutstanding(c.id),
      getDepositAccountsByMember(c.id),
      getMemberLoanHistory(c.id),
    ]);
    setOutstanding(o.status === 'fulfilled' ? o.value : null);
    if (o.status === 'rejected') console.warn('outstanding lookup failed', extractError(o.reason));
    setCpDeposits(d.status === 'fulfilled' ? d.value : []);
    if (d.status === 'rejected') console.warn('deposit accounts lookup failed', extractError(d.reason));
    setCpLoans(
      l.status === 'fulfilled'
        ? l.value.loans
            .map((row) => row.loan)
            // Only loans that can still accept a repayment line.
            .filter((ln) => ['active', 'in_arrears', 'restructured'].includes(ln.status))
        : []
    );
    if (l.status === 'rejected') console.warn('loan history lookup failed', extractError(l.reason));
  }

  async function confirmPost() {
    if (!cp) return;
    setPosting(true);
    setPostErr(null);
    try {
      const receipt = await createReceipt({
        counterparty_id: cp.id,
        channel,
        channel_ref: channel === 'cash' ? undefined : channelRef,
        channel_amount: channelAmountNum.toFixed(2),
        value_date: valueDate,
        narration: narration || undefined,
        lines: lines.map<CreateReceiptLineInput>((l) => ({
          kind: l.kind,
          amount: (parseFloat(l.amount) || 0).toFixed(2),
          target_account_id: l.target_account_id || undefined,
          fee_code: l.fee_code || undefined,
          narration: l.narration || undefined,
        })),
      });
      setPosted(receipt);
    } catch (e) {
      setPostErr(extractError(e));
    } finally {
      setPosting(false);
    }
  }

  // ─────────── Render ───────────

  // Till-session gate. Cash is blocked. Non-cash works regardless.
  const cashBlocked = !till?.has_open_session && channel === 'cash';

  if (posted) {
    return <PostedView receipt={posted} onReset={reset} />;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Servicing · Collection Desk</div>
          <h1>Collection Desk</h1>
          <div className="page-sub">
            Receipt every kind of money-in — savings, shares, loan repayments, fees — on a single slip.
          </div>
        </div>
        <div className="page-hd-actions">
          <TillBadge till={till} err={tillErr} />
        </div>
      </div>

      <StepBar step={step} disable={{ build: !cp, payment: lines.length === 0, confirm: subtotal === 0 || lines.length === 0 }} setStep={setStep} />

      {step === 'find' && (
        <FindStep onPick={pickCp} />
      )}

      {step !== 'find' && cp && (
        <div style={{ display: 'grid', gridTemplateColumns: '320px 1fr', gap: 12 }}>
          {/* Left rail — counterparty header + outstanding suggestions */}
          <div>
            <CpHeader cp={cp} onChange={reset} />
            {outstanding && (
              <OutstandingPanel
                outstanding={outstanding}
                onSuggest={(suggested) => {
                  setLines(suggested);
                  setStep('build');
                }}
              />
            )}
          </div>

          {/* Right rail — builder / payment / confirm */}
          <div>
            {step === 'build' && (
              <BuildStep
                lines={lines}
                setLines={setLines}
                fees={fees}
                deposits={cpDeposits}
                loans={cpLoans}
                onContinue={() => {
                  setChannelAmount(subtotal.toFixed(2));
                  setStep('payment');
                }}
              />
            )}
            {step === 'payment' && (
              <PaymentStep
                channel={channel}
                setChannel={setChannel}
                channelRef={channelRef}
                setChannelRef={setChannelRef}
                channelAmount={channelAmount}
                setChannelAmount={setChannelAmount}
                valueDate={valueDate}
                setValueDate={setValueDate}
                narration={narration}
                setNarration={setNarration}
                subtotal={subtotal}
                onBack={() => setStep('build')}
                onContinue={() => setStep('confirm')}
                cashBlocked={cashBlocked}
              />
            )}
            {step === 'confirm' && (
              <ConfirmStep
                cp={cp}
                lines={lines}
                channel={channel}
                channelRef={channelRef}
                channelAmount={channelAmountNum}
                valueDate={valueDate}
                narration={narration}
                till={till}
                posting={posting}
                err={postErr}
                onBack={() => setStep('payment')}
                onConfirm={confirmPost}
              />
            )}
          </div>
        </div>
      )}
    </div>
  );
}

// ─────────── Step bar ───────────

function StepBar({
  step, disable, setStep,
}: {
  step: Step;
  disable: { build: boolean; payment: boolean; confirm: boolean };
  setStep: (s: Step) => void;
}) {
  const steps: { id: Step; label: string }[] = [
    { id: 'find', label: '1 · Find member' },
    { id: 'build', label: '2 · Build receipt' },
    { id: 'payment', label: '3 · Payment details' },
    { id: 'confirm', label: '4 · Confirm & post' },
  ];
  return (
    <div className="fchips" style={{ marginBottom: 14 }}>
      {steps.map((s) => {
        const isDisabled = (s.id === 'build' && disable.build)
          || (s.id === 'payment' && disable.payment)
          || (s.id === 'confirm' && disable.confirm);
        return (
          <button
            key={s.id}
            type="button"
            className="fchip"
            data-active={step === s.id || undefined}
            onClick={() => !isDisabled && setStep(s.id)}
            disabled={isDisabled}
            style={isDisabled ? { opacity: 0.4, cursor: 'not-allowed' } : undefined}
          >
            {s.label}
          </button>
        );
      })}
    </div>
  );
}

// ─────────── Till badge ───────────

function TillBadge({ till, err }: { till: CurrentTillSession | null; err: string | null }) {
  if (err) return <span className="muted tiny">till status: error</span>;
  if (!till) return <span className="muted tiny">checking till…</span>;
  if (!till.has_open_session) {
    return (
      <a href="/cash-management" className="btn btn-sm" style={{ background: 'var(--neg, #c54)', color: 'white' }}>
        Open your till →
      </a>
    );
  }
  return (
    <span className="muted tiny">
      till <strong>{till.till_code}</strong> open · {till.till_name}
    </span>
  );
}

// ─────────── Step 1: find counterparty ───────────

function FindStep({ onPick }: { onPick: (cp: Counterparty) => void }) {
  const [q, setQ] = useState('');
  const [results, setResults] = useState<Counterparty[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function search() {
    if (!q.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const r = await listCounterparties({ q: q.trim(), limit: 20 });
      setResults(r.counterparties);
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Find member</h3>
        <span className="card-sub">By name, member number, phone, email, or ID number.</span>
      </div>
      <div className="card-body">
        <form
          onSubmit={(e) => { e.preventDefault(); void search(); }}
          style={{ display: 'flex', gap: 6, marginBottom: 12 }}
        >
          <input
            className="input"
            style={{ flex: 1, fontSize: 14, height: 32 }}
            placeholder="Search — Esther, M-2026-00012, +254700…, ID 12345678"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            autoFocus
          />
          <button className="btn btn-accent" type="submit" disabled={busy || !q.trim()}>
            {busy ? 'Searching…' : 'Search'}
          </button>
        </form>
        {err && <div className="alert alert-error">{err}</div>}
        {results && results.length === 0 && (
          <div className="empty">No counterparties match. Try a different term.</div>
        )}
        {results && results.length > 0 && (
          <table className="tbl">
            <thead>
              <tr>
                <th>Name</th>
                <th>CP # / legacy</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {results.map((c) => (
                <tr key={c.id}>
                  <td><strong>{c.display_name}</strong> <span className="muted tiny">· {c.kind}</span></td>
                  <td className="tiny-mono">
                    {c.cp_number}
                    {c.legacy_id && <div className="muted tiny mono">{c.legacy_id}</div>}
                  </td>
                  <td><span className="badge">{c.status}</span></td>
                  <td>
                    <button className="btn btn-sm btn-accent" onClick={() => onPick(c)}>Select →</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─────────── Counterparty header + outstanding panel ───────────

function CpHeader({ cp, onChange }: { cp: Counterparty; onChange: () => void }) {
  return (
    <div className="card" style={{ marginBottom: 12 }}>
      <div className="card-hd">
        <h3 style={{ marginBottom: 0 }}>{cp.display_name}</h3>
        <div className="card-hd-actions">
          <button className="btn btn-sm" onClick={onChange}>Change</button>
        </div>
      </div>
      <div className="card-body">
        <div className="tiny-mono">{cp.cp_number}</div>
        {cp.legacy_id && <div className="muted tiny mono">{cp.legacy_id}</div>}
        <div style={{ marginTop: 6 }}>
          <span className="badge">{cp.status}</span> <span className="muted tiny">· {cp.kind}</span>
        </div>
      </div>
    </div>
  );
}

function OutstandingPanel({
  outstanding,
  onSuggest,
}: {
  outstanding: CounterpartyOutstanding;
  onSuggest: (lines: LineDraft[]) => void;
}) {
  const hasArrears = outstanding.loan_arrears.length > 0;
  const hasShortfall = (outstanding.share_shortfall?.shortfall_shares ?? 0) > 0;
  const hasAnything = hasArrears || hasShortfall;
  if (!hasAnything) {
    return (
      <div className="card">
        <div className="card-hd"><h3 style={{ marginBottom: 0 }}>Outstanding</h3></div>
        <div className="card-body">
          <div className="muted tiny">Nothing outstanding.</div>
        </div>
      </div>
    );
  }
  return (
    <div className="card">
      <div className="card-hd">
        <h3 style={{ marginBottom: 0 }}>Outstanding</h3>
        <span className="card-sub">Suggested total: KES {outstanding.total_suggested}</span>
      </div>
      <div className="card-body" style={{ display: 'grid', gap: 8 }}>
        {hasArrears && (
          <div>
            <div className="muted tiny">Loan arrears</div>
            {outstanding.loan_arrears.map((a) => (
              <div key={a.loan_id} style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }}>
                <span className="tiny-mono">{a.loan_no} <span className="muted">· {a.days_past_due}d</span></span>
                <span className="mono">KES {a.arrears_amount}</span>
              </div>
            ))}
          </div>
        )}
        {hasShortfall && outstanding.share_shortfall && (
          <div>
            <div className="muted tiny">Share shortfall</div>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12 }}>
              <span>{outstanding.share_shortfall.shortfall_shares} shares below policy</span>
              <span className="mono">KES {outstanding.share_shortfall.shortfall_kes}</span>
            </div>
          </div>
        )}
        <button
          className="btn btn-sm btn-accent"
          onClick={() => {
            const suggested: LineDraft[] = [];
            for (const a of outstanding.loan_arrears) {
              suggested.push({
                rowKey: rowKey(),
                kind: 'loan_repayment',
                amount: a.arrears_amount,
                target_account_id: a.loan_id,
                narration: `Arrears clear · ${a.loan_no}`,
              });
            }
            if (outstanding.share_shortfall && outstanding.share_shortfall.shortfall_shares > 0) {
              suggested.push({
                rowKey: rowKey(),
                kind: 'share_purchase',
                amount: outstanding.share_shortfall.shortfall_kes,
                narration: 'Top up to minimum share holding',
              });
            }
            onSuggest(suggested);
          }}
        >
          Suggest from outstanding
        </button>
      </div>
    </div>
  );
}

// ─────────── Step 2: build receipt ───────────

function BuildStep({
  lines, setLines, fees, deposits, loans, onContinue,
}: {
  lines: LineDraft[];
  setLines: (l: LineDraft[]) => void;
  fees: FeeCatalogEntry[];
  deposits: MemberDepositItem[];
  loans: Loan[];
  onContinue: () => void;
}) {
  function add(kind: ReceiptLineKind) {
    // Pre-pick the first valid target so each row opens with a real
    // selection — saves the cashier a click + makes the "no targets
    // available" state obvious (the row appears with an empty select).
    if (kind === 'fee' && fees.length > 0) {
      const f = fees[0];
      setLines([...lines, {
        rowKey: rowKey(), kind,
        fee_code: f.code,
        amount: f.amount_editable ? '' : f.amount_default,
      }]);
      return;
    }
    if (kind === 'savings_deposit' && deposits.length > 0) {
      setLines([...lines, { rowKey: rowKey(), kind, target_account_id: deposits[0].account.id, amount: '' }]);
      return;
    }
    if (kind === 'loan_repayment' && loans.length > 0) {
      setLines([...lines, { rowKey: rowKey(), kind, target_account_id: loans[0].id, amount: '' }]);
      return;
    }
    setLines([...lines, { rowKey: rowKey(), kind, amount: '' }]);
  }
  function update(i: number, patch: Partial<LineDraft>) {
    const next = lines.slice();
    next[i] = { ...next[i], ...patch };
    setLines(next);
  }
  function remove(i: number) {
    setLines(lines.filter((_, j) => j !== i));
  }
  const subtotal = lines.reduce((acc, l) => acc + (parseFloat(l.amount || '0') || 0), 0);

  return (
    <div className="card">
      <div className="card-hd">
        <h3>Receipt lines</h3>
        <span className="card-sub">{lines.length} line{lines.length === 1 ? '' : 's'} · subtotal KES {subtotal.toFixed(2)}</span>
        <div className="card-hd-actions">
          <div className="fchips">
            {(['savings_deposit', 'share_purchase', 'loan_repayment', 'fee'] as ReceiptLineKind[]).map((k) => (
              <button key={k} type="button" className="fchip" onClick={() => add(k)}>
                + {LINE_KIND_LABELS[k]}
              </button>
            ))}
          </div>
        </div>
      </div>
      <div className="card-body">
        {lines.length === 0 ? (
          <div className="empty">No lines yet. Use a + chip above to add the first one.</div>
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th>#</th>
                <th>Kind</th>
                <th>Target / fee code</th>
                <th>Amount (KES)</th>
                <th>Narration</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {lines.map((l, i) => (
                <tr key={l.rowKey}>
                  <td className="tiny-mono">{i + 1}</td>
                  <td>{LINE_KIND_LABELS[l.kind]}</td>
                  <td>
                    {l.kind === 'savings_deposit' && (
                      deposits.length > 0 ? (
                        <select
                          className="input"
                          style={{ height: 28, fontSize: 12, maxWidth: 260 }}
                          value={l.target_account_id ?? ''}
                          onChange={(e) => update(i, { target_account_id: e.target.value })}
                        >
                          {deposits.map((d) => (
                            <option key={d.account.id} value={d.account.id}>
                              {d.product.code} · {d.account.account_no} (bal KES {d.account.current_balance})
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="muted tiny">No savings accounts for this member.</span>
                      )
                    )}
                    {l.kind === 'loan_repayment' && (
                      loans.length > 0 ? (
                        <select
                          className="input"
                          style={{ height: 28, fontSize: 12, maxWidth: 260 }}
                          value={l.target_account_id ?? ''}
                          onChange={(e) => update(i, { target_account_id: e.target.value })}
                        >
                          {loans.map((ln) => (
                            <option key={ln.id} value={ln.id}>
                              {ln.loan_no} · {ln.status} · bal KES {ln.principal_balance}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <span className="muted tiny">No active loans for this member.</span>
                      )
                    )}
                    {l.kind === 'fee' && (
                      fees.length > 0 ? (
                        <select
                          className="input"
                          style={{ height: 28, fontSize: 12 }}
                          value={l.fee_code ?? ''}
                          onChange={(e) => {
                            const f = fees.find((x) => x.code === e.target.value);
                            update(i, {
                              fee_code: e.target.value,
                              // When picking a fixed-amount fee, snap
                              // the row's amount to the catalog default.
                              ...(f && !f.amount_editable ? { amount: f.amount_default } : {}),
                            });
                          }}
                        >
                          {fees.map((f) => (
                            <option key={f.code} value={f.code}>
                              {f.label}{!f.amount_editable ? ` (KES ${f.amount_default})` : ''}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <input
                          className="input"
                          style={{ height: 28, fontSize: 12 }}
                          placeholder="fee code (catalog empty)"
                          value={l.fee_code ?? ''}
                          onChange={(e) => update(i, { fee_code: e.target.value })}
                        />
                      )
                    )}
                    {l.kind === 'share_purchase' && (
                      <span className="muted tiny">No target — issues against the CP's share account.</span>
                    )}
                  </td>
                  <td>
                    {(() => {
                      const feeEntry = l.kind === 'fee' && l.fee_code
                        ? fees.find((f) => f.code === l.fee_code)
                        : undefined;
                      const locked = feeEntry ? !feeEntry.amount_editable : false;
                      return (
                        <input
                          className="input mono"
                          style={{ height: 28, width: 110, textAlign: 'right', background: locked ? '#f5f5f5' : undefined }}
                          type="number"
                          step="0.01"
                          min="0"
                          value={l.amount}
                          readOnly={locked}
                          title={locked ? 'Fixed-amount fee — edit in fee catalog to change' : undefined}
                          onChange={(e) => update(i, { amount: e.target.value })}
                        />
                      );
                    })()}
                  </td>
                  <td>
                    <input
                      className="input"
                      style={{ height: 28, fontSize: 12 }}
                      placeholder="optional"
                      value={l.narration ?? ''}
                      onChange={(e) => update(i, { narration: e.target.value })}
                    />
                  </td>
                  <td>
                    <button className="btn btn-sm" onClick={() => remove(i)}>Remove</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <div style={{ marginTop: 12, display: 'flex', justifyContent: 'flex-end' }}>
          <button
            className="btn btn-accent"
            onClick={onContinue}
            disabled={lines.length === 0 || subtotal <= 0}
          >
            Continue to payment →
          </button>
        </div>
      </div>
    </div>
  );
}

// ─────────── Step 3: payment ───────────

function PaymentStep(props: {
  channel: ReceiptChannel;
  setChannel: (c: ReceiptChannel) => void;
  channelRef: string;
  setChannelRef: (s: string) => void;
  channelAmount: string;
  setChannelAmount: (s: string) => void;
  valueDate: string;
  setValueDate: (s: string) => void;
  narration: string;
  setNarration: (s: string) => void;
  subtotal: number;
  onBack: () => void;
  onContinue: () => void;
  cashBlocked: boolean;
}) {
  const channelAmountNum = parseFloat(props.channelAmount || '0') || 0;
  const subtotalMismatch = Math.abs(channelAmountNum - props.subtotal) > 0.005;
  const refRequired = props.channel !== 'cash';
  const refMissing = refRequired && !props.channelRef.trim();
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Payment details</h3>
        <span className="card-sub">Channel + reference. The amount must match the receipt subtotal.</span>
      </div>
      <div className="card-body" style={{ display: 'grid', gap: 10 }}>
        <div>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Channel</div>
          <div className="fchips">
            {CHANNELS.map((c) => (
              <button
                key={c.value}
                type="button"
                className="fchip"
                data-active={props.channel === c.value || undefined}
                onClick={() => props.setChannel(c.value)}
              >
                {c.label}
              </button>
            ))}
          </div>
          {props.cashBlocked && (
            <div className="alert alert-error" style={{ marginTop: 8 }}>
              No open till session — open your till before posting cash receipts.
            </div>
          )}
        </div>
        {refRequired && (
          <Field label={`${CHANNELS.find((c) => c.value === props.channel)?.label} reference`}>
            <input
              className="input"
              placeholder="MPS-…, BANK-…, cheque #"
              value={props.channelRef}
              onChange={(e) => props.setChannelRef(e.target.value)}
            />
          </Field>
        )}
        <Field label="Channel amount (KES)">
          <input
            className="input mono"
            type="number"
            step="0.01"
            min="0"
            value={props.channelAmount}
            onChange={(e) => props.setChannelAmount(e.target.value)}
          />
          {subtotalMismatch && (
            <div className="muted tiny" style={{ color: 'var(--neg, #c54)' }}>
              Subtotal is KES {props.subtotal.toFixed(2)} — must match.
            </div>
          )}
        </Field>
        <Field label="Value date">
          <input
            className="input"
            type="date"
            value={props.valueDate}
            onChange={(e) => props.setValueDate(e.target.value)}
            max={new Date().toISOString().slice(0, 10)}
          />
        </Field>
        <Field label="Narration (optional)">
          <input
            className="input"
            value={props.narration}
            onChange={(e) => props.setNarration(e.target.value)}
          />
        </Field>
        <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 8 }}>
          <button className="btn btn-sm" onClick={props.onBack}>← Back</button>
          <button
            className="btn btn-accent"
            onClick={props.onContinue}
            disabled={props.cashBlocked || refMissing || subtotalMismatch || channelAmountNum <= 0}
          >
            Continue to confirm →
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block' }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

// ─────────── Step 4: confirm ───────────

function ConfirmStep(props: {
  cp: Counterparty;
  lines: LineDraft[];
  channel: ReceiptChannel;
  channelRef: string;
  channelAmount: number;
  valueDate: string;
  narration: string;
  till: CurrentTillSession | null;
  posting: boolean;
  err: string | null;
  onBack: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className="card">
      <div className="card-hd">
        <h3>Confirm receipt</h3>
        <span className="card-sub">Review and post.</span>
      </div>
      <div className="card-body">
        <ReceiptPreview {...props} />
        {props.err && <div className="alert alert-error" style={{ marginTop: 12 }}>{props.err}</div>}
        <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 12 }}>
          <button className="btn btn-sm" onClick={props.onBack} disabled={props.posting}>← Back</button>
          <button className="btn btn-accent" onClick={props.onConfirm} disabled={props.posting}>
            {props.posting ? 'Posting…' : 'Post receipt'}
          </button>
        </div>
      </div>
    </div>
  );
}

function ReceiptPreview(props: {
  cp: Counterparty;
  lines: LineDraft[];
  channel: ReceiptChannel;
  channelRef: string;
  channelAmount: number;
  valueDate: string;
  narration: string;
  till: CurrentTillSession | null;
}) {
  return (
    <div className="card" style={{ background: '#fafafa', border: '1px dashed var(--border)' }}>
      <div className="card-body">
        <div style={{ textAlign: 'center', marginBottom: 12 }}>
          <strong>RECEIPT PREVIEW</strong>
          {props.till?.till_code && (
            <div className="muted tiny">till {props.till.till_code}</div>
          )}
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
          <div>
            <div className="muted tiny">Member</div>
            <strong>{props.cp.display_name}</strong>
            <div className="tiny-mono">{props.cp.cp_number}</div>
          </div>
          <div style={{ textAlign: 'right' }}>
            <div className="muted tiny">Value date</div>
            <div className="tiny-mono">{props.valueDate}</div>
          </div>
        </div>
        <table className="tbl" style={{ marginTop: 8 }}>
          <thead>
            <tr>
              <th>#</th>
              <th>Description</th>
              <th style={{ textAlign: 'right' }}>Amount (KES)</th>
            </tr>
          </thead>
          <tbody>
            {props.lines.map((l, i) => (
              <tr key={l.rowKey}>
                <td className="tiny-mono">{i + 1}</td>
                <td>
                  {LINE_KIND_LABELS[l.kind]}
                  {l.narration && <div className="muted tiny">{l.narration}</div>}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{(parseFloat(l.amount) || 0).toFixed(2)}</td>
              </tr>
            ))}
            <tr style={{ borderTop: '2px solid var(--border)' }}>
              <td colSpan={2} style={{ textAlign: 'right' }}><strong>Total</strong></td>
              <td className="mono" style={{ textAlign: 'right' }}><strong>{props.channelAmount.toFixed(2)}</strong></td>
            </tr>
          </tbody>
        </table>
        <div style={{ marginTop: 10, display: 'flex', justifyContent: 'space-between' }}>
          <div>
            <div className="muted tiny">Channel</div>
            <div className="tiny-mono">{props.channel}{props.channelRef && ' · ' + props.channelRef}</div>
          </div>
          {props.narration && (
            <div style={{ textAlign: 'right' }}>
              <div className="muted tiny">Narration</div>
              <div className="tiny">{props.narration}</div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ─────────── Posted view ───────────

function PostedView({ receipt, onReset }: { receipt: ApiReceipt; onReset: () => void }) {
  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Servicing · Collection Desk</div>
          <h1>Receipt posted</h1>
          <div className="page-sub">Serial <strong>{receipt.serial}</strong></div>
        </div>
        <div className="page-hd-actions">
          <a className="btn btn-sm" href={`/collect/receipts/${receipt.id}`}>View receipt</a>
          <button className="btn btn-sm btn-accent" onClick={onReset}>Start another receipt</button>
        </div>
      </div>
      <div className="card">
        <div className="card-hd">
          <h3>Per-line status</h3>
          <span className="card-sub">Each line was queued for approval. Check the approvals inbox to clear them.</span>
        </div>
        <div className="card-body">
          <table className="tbl">
            <thead>
              <tr>
                <th>#</th>
                <th>Kind</th>
                <th>Amount</th>
                <th>Status</th>
                <th>Approval</th>
              </tr>
            </thead>
            <tbody>
              {(receipt.lines ?? []).map((l) => (
                <tr key={l.id}>
                  <td className="tiny-mono">{l.line_no}</td>
                  <td>{LINE_KIND_LABELS[l.kind]}</td>
                  <td className="mono">{l.amount}</td>
                  <td><span className="badge">{l.status}</span></td>
                  <td className="tiny-mono">
                    {l.approval_id
                      ? <a href={`/cash-approvals#${l.approval_id}`}>{l.approval_id.slice(0, 8)}…</a>
                      : <span className="muted">—</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
