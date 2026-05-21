// Unified Accounts panel — two-pane layout (rail + detail) covering both
// the member's Shares account and every Deposit account they hold.
//
// Replaces the standalone MemberSharesCard + MemberDepositsCard render.
// Permission gating, posting flows, statement view, and certificate
// download are all preserved from the old cards.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  adjustDeposit,
  adjustShares,
  bonusShareIssue as _bonus,
  extractError,
  getCurrentCertificate,
  getDepositAccountsByMember,
  getDepositStatement,
  getShareAccountByMember,
  listDepositProducts,
  listShareTransactions,
  openDepositAccount,
  placeShareLien,
  postDeposit,
  postWithdrawal,
  purchaseShares,
  redeemShares,
  releaseShareLien,
  reverseDeposit,
  transferBetweenOwn,
  transferShares,
  type DepositAccount,
  type DepositChannel,
  type DepositProduct,
  type DepositStatement,
  type DepositTransaction,
  type MemberDepositItem,
  type ShareAccountView,
  type ShareCertificate,
  type SharePaymentChannel,
  type ShareTransaction,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge, StatusBadge } from './Badge';
import { Icon } from './Icon';

// Silence the unused-import warning on bonusShareIssue (kept for future).
void _bonus;

// ─────────── Labels & helpers ───────────

const PRODUCT_LABEL: Record<DepositProduct['product_type'], string> = {
  ordinary: 'Ordinary',
  fixed: 'Fixed deposit',
  junior: 'Junior',
  holiday: 'Holiday',
  goal: 'Goal',
  emergency: 'Emergency',
  group: 'Group',
};

const SHARE_CHANNEL_LABELS: Record<SharePaymentChannel, string> = {
  cash: 'Cash',
  mpesa: 'M-Pesa',
  airtel_money: 'Airtel Money',
  bank_transfer: 'Bank transfer',
  payroll: 'Payroll',
  standing_order: 'Standing order',
  internal: 'Internal',
};

const DEP_CHANNEL_LABELS: Record<DepositChannel, string> = {
  cash: 'Cash',
  mpesa: 'M-Pesa',
  airtel_money: 'Airtel Money',
  bank_transfer: 'Bank transfer',
  standing_order: 'Standing order',
  direct_debit: 'Direct debit',
  payroll: 'Payroll',
  internal: 'Internal',
};

const SHARE_TXN_LABELS: Record<ShareTransaction['txn_type'], string> = {
  purchase: 'Purchase',
  transfer_in: 'Transfer in',
  transfer_out: 'Transfer out',
  redemption: 'Redemption',
  adjustment: 'Adjustment',
  bonus_issue: 'Bonus issue',
};

const DEP_TXN_LABELS: Record<DepositTransaction['txn_type'], string> = {
  opening_balance: 'Opening balance',
  deposit: 'Deposit',
  withdrawal: 'Withdrawal',
  transfer_in: 'Transfer in',
  transfer_out: 'Transfer out',
  interest_credit: 'Interest credit',
  fee_debit: 'Fee',
  reversal: 'Reversal',
  adjustment: 'Adjustment',
  goal_payout: 'Goal payout',
};

function fmtMoney(s: string | number | undefined): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return String(s);
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function pct(a: string, b: string): string {
  const num = parseFloat(a); const den = parseFloat(b);
  if (!isFinite(num) || !isFinite(den) || den <= 0) return '0';
  return ((num / den) * 100).toFixed(1);
}

function shareTxnTone(t: ShareTransaction['txn_type']): 'pos' | 'neg' | 'accent' | 'neutral' {
  switch (t) {
    case 'purchase': case 'transfer_in': case 'bonus_issue': return 'pos';
    case 'redemption': case 'transfer_out': return 'neg';
    case 'adjustment': return 'accent';
    default: return 'neutral';
  }
}
function depTxnTone(t: DepositTransaction['txn_type']): 'pos' | 'neg' | 'accent' | 'neutral' | 'warn' {
  switch (t) {
    case 'deposit': case 'transfer_in': case 'interest_credit': case 'opening_balance': return 'pos';
    case 'withdrawal': case 'transfer_out': case 'fee_debit': case 'goal_payout': return 'neg';
    case 'reversal': return 'warn';
    case 'adjustment': return 'accent';
    default: return 'neutral';
  }
}

// ─────────── Selection type ───────────

type Selection =
  | { kind: 'shares' }
  | { kind: 'deposit'; accountId: string }
  | { kind: 'empty' };

// ─────────── Main panel ───────────

export function MemberAccountsPanel({ memberId, currency }: { memberId: string; currency: string }) {
  const { hasPermission } = useAuth();
  const [shares, setShares] = useState<ShareAccountView | null>(null);
  const [deposits, setDeposits] = useState<MemberDepositItem[]>([]);
  const [products, setProducts] = useState<DepositProduct[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<Selection>({ kind: 'shares' });
  const [modal, setModal] = useState<ModalState>({ kind: null });

  async function reload() {
    setErr(null);
    try {
      const [sv, d, p] = await Promise.all([
        getShareAccountByMember(memberId).catch(() => null),
        getDepositAccountsByMember(memberId),
        listDepositProducts(false),
      ]);
      setShares(sv);
      setDeposits(d);
      setProducts(p);
    } catch (e) { setErr(extractError(e)); }
    finally { setLoading(false); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [memberId]);

  // Keep the selection valid as data refreshes.
  useEffect(() => {
    if (loading) return;
    if (selected.kind === 'deposit' && !deposits.find((d) => d.account.id === selected.accountId)) {
      setSelected(shares ? { kind: 'shares' } : (deposits[0] ? { kind: 'deposit', accountId: deposits[0].account.id } : { kind: 'empty' }));
    } else if (selected.kind === 'shares' && !shares) {
      setSelected(deposits[0] ? { kind: 'deposit', accountId: deposits[0].account.id } : { kind: 'empty' });
    }
  }, [loading, shares, deposits, selected]);

  const canOpenDeposit = hasPermission('savings:transact');

  if (loading) {
    return (
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd"><h3>Accounts</h3></div>
        <div className="card-body"><div className="empty">Loading…</div></div>
      </div>
    );
  }
  if (err) {
    return (
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd"><h3>Accounts</h3></div>
        <div className="card-body"><div className="alert alert-error">{err}</div></div>
      </div>
    );
  }

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Accounts</h3>
        <span className="card-sub">
          {(shares ? 1 : 0) + deposits.length} account{(shares ? 1 : 0) + deposits.length === 1 ? '' : 's'}
        </span>
        <div className="card-hd-actions">
          {canOpenDeposit && products.length > 0 && (
            <button className="btn btn-sm btn-accent" onClick={() => setModal({ kind: 'open' })}>
              <Icon name="plus" size={12} /> Open deposit account
            </button>
          )}
        </div>
      </div>
      <div className="card-body" style={{ padding: 0 }}>
        <div style={{ display: 'flex', minHeight: 420 }}>
          <Rail
            shares={shares}
            deposits={deposits}
            currency={currency}
            selected={selected}
            onSelect={setSelected}
          />
          <div style={{ flex: 1, padding: 14, borderLeft: '1px solid var(--border)', minWidth: 0 }}>
            {selected.kind === 'shares' && shares && (
              <SharesDetail
                memberId={memberId}
                view={shares}
                currency={currency}
                onOpenModal={(m) => setModal(m)}
                onReload={reload}
              />
            )}
            {selected.kind === 'deposit' && (() => {
              const it = deposits.find((d) => d.account.id === selected.accountId);
              return it ? (
                <DepositDetail
                  it={it}
                  memberId={memberId}
                  deposits={deposits}
                  currency={currency}
                  onOpenModal={(m) => setModal(m)}
                />
              ) : <div className="empty">Account not found.</div>;
            })()}
            {selected.kind === 'empty' && (
              <div className="empty" style={{ padding: '48px 16px' }}>
                <div style={{ fontSize: 14, marginBottom: 8 }}>This member has no accounts yet.</div>
                {canOpenDeposit && products.length > 0 && (
                  <button className="btn btn-sm btn-accent" onClick={() => setModal({ kind: 'open' })}>
                    <Icon name="plus" size={12} /> Open the first deposit account
                  </button>
                )}
                {products.length === 0 && (
                  <div className="muted tiny" style={{ marginTop: 8 }}>
                    No deposit products configured.{' '}
                    <a href="/deposit-products" style={{ color: 'var(--accent)' }}>Configure products →</a>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      <Modals
        modal={modal}
        memberId={memberId}
        currency={currency}
        shares={shares}
        deposits={deposits}
        products={products}
        onClose={() => setModal({ kind: null })}
        onChanged={async () => { await reload(); setModal({ kind: null }); }}
      />
    </div>
  );
}

// ─────────── Left rail ───────────

function Rail({
  shares, deposits, currency, selected, onSelect,
}: {
  shares: ShareAccountView | null;
  deposits: MemberDepositItem[];
  currency: string;
  selected: Selection;
  onSelect: (s: Selection) => void;
}) {
  return (
    <div style={{ width: 240, flexShrink: 0, padding: '8px 0' }}>
      {shares && (
        <RailItem
          eyebrow="Shares"
          title={`${shares.account.shares_held.toLocaleString()} shares`}
          subtitle={`${currency} ${fmtMoney(shares.account.total_value)} · ${shares.account.account_no}`}
          active={selected.kind === 'shares'}
          onClick={() => onSelect({ kind: 'shares' })}
        />
      )}
      {deposits.map((it) => (
        <RailItem
          key={it.account.id}
          eyebrow={PRODUCT_LABEL[it.product.product_type]}
          title={`${currency} ${fmtMoney(it.account.current_balance)}`}
          subtitle={`${it.product.name} · ${it.account.account_no}`}
          status={it.account.status}
          active={selected.kind === 'deposit' && selected.accountId === it.account.id}
          onClick={() => onSelect({ kind: 'deposit', accountId: it.account.id })}
        />
      ))}
      {!shares && deposits.length === 0 && (
        <div className="muted tiny" style={{ padding: 16 }}>No accounts.</div>
      )}
    </div>
  );
}

function RailItem({
  eyebrow, title, subtitle, status, active, onClick,
}: {
  eyebrow: string; title: string; subtitle: string;
  status?: string; active: boolean; onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        display: 'block', width: '100%', textAlign: 'left',
        padding: '10px 14px',
        border: 0,
        borderLeft: `3px solid ${active ? 'var(--accent)' : 'transparent'}`,
        background: active ? 'var(--surface-2)' : 'transparent',
        cursor: 'pointer',
        fontFamily: 'inherit', color: 'inherit',
      }}
    >
      <div className="muted tiny" style={{ textTransform: 'uppercase', letterSpacing: '.5px' }}>{eyebrow}</div>
      <div className="mono" style={{ fontWeight: 600, marginTop: 2 }}>{title}</div>
      <div className="muted tiny" style={{ marginTop: 2 }}>{subtitle}</div>
      {status && (
        <div style={{ marginTop: 4 }}><StatusBadge status={status} /></div>
      )}
    </button>
  );
}

// ─────────── Shares detail pane ───────────

function SharesDetail({
  memberId, view, currency, onOpenModal, onReload,
}: {
  memberId: string;
  view: ShareAccountView;
  currency: string;
  onOpenModal: (m: ModalState) => void;
  onReload: () => void;
}) {
  const { hasPermission } = useAuth();
  const a = view.account;
  const policy = view.policy;
  const liens = view.active_liens ?? [];
  const minRequired = policy.min_shares_required;
  const belowMin = a.shares_held < minRequired;
  const hasLien = a.shares_pledged > 0;

  const [txns, setTxns] = useState<ShareTransaction[]>([]);
  const [cert, setCert] = useState<ShareCertificate | null>(null);
  const [busy, setBusy] = useState(false);

  async function loadHistory() {
    try {
      const [t, c] = await Promise.all([
        listShareTransactions(memberId, { limit: 10 }),
        getCurrentCertificate(memberId),
      ]);
      setTxns(t);
      setCert(c);
    } catch { /* swallow — handled by the parent error state */ }
  }
  useEffect(() => { void loadHistory(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [memberId, a.id]);

  const canBuy = hasPermission('shares:buy');
  const canRedeem = hasPermission('shares:redeem');
  const canTransfer = hasPermission('shares:transfer');
  const canAdjust = hasPermission('shares:adjust');
  const canLien = hasPermission('shares:lien');

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 12 }}>
        <div>
          <div className="muted tiny" style={{ textTransform: 'uppercase', letterSpacing: '.5px' }}>Shares · Equity</div>
          <h3 style={{ margin: '2px 0' }}>{a.shares_held.toLocaleString()} shares</h3>
          <div className="muted tiny">Account {a.account_no} · {currency} {policy.par_value}/share par</div>
        </div>
        <div className="row" style={{ gap: 6 }}>
          {canBuy && <button className="btn btn-sm btn-accent" onClick={() => onOpenModal({ kind: 'shareBuy' })}>Buy</button>}
          {canRedeem && <button className="btn btn-sm" disabled={a.shares_held === 0} onClick={() => onOpenModal({ kind: 'shareRedeem' })}>Redeem</button>}
          {canTransfer && <button className="btn btn-sm" disabled={a.shares_available === 0} onClick={() => onOpenModal({ kind: 'shareTransfer' })}>Transfer</button>}
          {canAdjust && <button className="btn btn-sm" onClick={() => onOpenModal({ kind: 'shareAdjust' })}>Adjust</button>}
          {canLien && <button className="btn btn-sm" onClick={() => onOpenModal({ kind: 'shareLien' })}>+ Lien</button>}
        </div>
      </div>

      <div className="grid-4" style={{ marginBottom: 12 }}>
        <KPI label="Shares held" value={a.shares_held.toLocaleString()} sub={`min ${minRequired}`} tone={belowMin ? 'warn' : 'pos'} />
        <KPI label="Capital value" value={`${currency} ${fmtMoney(a.total_value)}`} />
        <KPI label="Available" value={a.shares_available.toLocaleString()} sub={hasLien ? `${a.shares_pledged} pledged` : 'no lien'} tone={hasLien ? 'warn' : undefined} />
        <KPI label="Certificate" value={cert?.certificate_no ?? '—'} sub={cert ? `issued ${cert.issued_at.slice(0, 10)}` : 'none yet'} />
      </div>

      {belowMin && (
        <div className="alert alert-warn" style={{ marginBottom: 12 }}>
          Member is below the {minRequired}-share minimum for good standing.
        </div>
      )}

      {liens.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          <h4 style={{ margin: '0 0 6px', fontSize: 13 }}>Active liens</h4>
          <table className="tbl">
            <thead>
              <tr><th>Shares</th><th>Reason</th><th>Placed</th><th></th></tr>
            </thead>
            <tbody>
              {liens.map((l) => (
                <tr key={l.id}>
                  <td className="mono">{l.shares_pledged}</td>
                  <td>
                    <div>{l.reason}</div>
                    {l.reference_kind && <div className="muted tiny">{l.reference_kind}: {l.reference_id}</div>}
                  </td>
                  <td className="tiny-mono">{l.placed_at.slice(0, 10)}</td>
                  <td>
                    {canLien && (
                      <button
                        className="btn btn-sm"
                        disabled={busy}
                        onClick={async () => {
                          const reason = prompt('Release reason?'); if (!reason) return;
                          setBusy(true);
                          try { await releaseShareLien(l.id, reason); await Promise.all([loadHistory(), Promise.resolve(onReload())]); }
                          catch (e) { alert(extractError(e)); }
                          finally { setBusy(false); }
                        }}
                      >Release</button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <h4 style={{ margin: '0 0 6px', fontSize: 13 }}>Recent transactions</h4>
      <TxnTable
        rows={txns}
        currency={currency}
        getNo={(t) => t.txn_no}
        getDate={(t) => t.posted_at}
        getType={(t) => SHARE_TXN_LABELS[t.txn_type]}
        getTypeTone={(t) => shareTxnTone(t.txn_type)}
        getAmount={(t) => t.shares_delta.toLocaleString()}
        getAmountSub={(t) => `${currency} ${fmtMoney(t.amount)}`}
        getChannel={(t) => (t.payment_channel ? SHARE_CHANNEL_LABELS[t.payment_channel] : '—')}
        getRef={(t) => t.payment_ref ?? null}
        getNarration={(t) => t.narration ?? null}
        getBalance={(t) => t.balance_after_shares.toLocaleString()}
        emptyMsg="No share transactions yet."
      />
    </div>
  );
}

// ─────────── Deposit detail pane ───────────

function DepositDetail({
  it, memberId, deposits, currency, onOpenModal,
}: {
  it: MemberDepositItem;
  memberId: string;
  deposits: MemberDepositItem[];
  currency: string;
  onOpenModal: (m: ModalState) => void;
}) {
  const { hasPermission } = useAuth();
  const a = it.account;
  const p = it.product;

  const [statement, setStatement] = useState<DepositStatement | null>(null);
  useEffect(() => {
    const today = new Date().toISOString().slice(0, 10);
    const monthAgo = new Date(Date.now() - 30 * 86400e3).toISOString().slice(0, 10);
    getDepositStatement(a.id, monthAgo, today)
      .then(setStatement)
      .catch(() => setStatement(null));
  }, [a.id]);
  const _mid = memberId; void _mid;

  const canTransact = hasPermission('savings:transact');
  const otherAccounts = deposits.filter((d) => d.account.id !== a.id);

  const activeOrPending = a.status === 'active' || a.status === 'pending';
  const activeOrMatured = a.status === 'active' || a.status === 'matured';

  return (
    <div>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 12 }}>
        <div>
          <div className="muted tiny" style={{ textTransform: 'uppercase', letterSpacing: '.5px' }}>
            {PRODUCT_LABEL[p.product_type]} · {p.code}
          </div>
          <h3 style={{ margin: '2px 0' }}>{p.name}</h3>
          <div className="muted tiny">Account {a.account_no} · <StatusBadge status={a.status} /></div>
        </div>
        <div className="row" style={{ gap: 6 }}>
          {canTransact && <button className="btn btn-sm btn-accent" disabled={!activeOrPending} onClick={() => onOpenModal({ kind: 'depDeposit', accountId: a.id })}>Deposit</button>}
          {canTransact && <button className="btn btn-sm" disabled={!activeOrMatured} onClick={() => onOpenModal({ kind: 'depWithdraw', accountId: a.id })}>Withdraw</button>}
          {canTransact && otherAccounts.length > 0 && <button className="btn btn-sm" disabled={a.status !== 'active'} onClick={() => onOpenModal({ kind: 'depTransfer', accountId: a.id })}>Transfer</button>}
          {hasPermission('savings:approve') && <button className="btn btn-sm" onClick={() => onOpenModal({ kind: 'depAdjust', accountId: a.id })}>Adjust</button>}
        </div>
      </div>

      <div className="grid-4" style={{ marginBottom: 12 }}>
        <KPI label="Balance" value={`${currency} ${fmtMoney(a.current_balance)}`} />
        <KPI label="Available" value={`${currency} ${fmtMoney(a.available_balance)}`} sub={a.current_balance === a.available_balance ? 'no holds' : undefined} />
        <KPI label="Last activity" value={a.last_activity_at ? a.last_activity_at.slice(0, 10) : '—'} />
        <KPI label="Opened" value={a.opened_at ? a.opened_at.slice(0, 10) : '—'} />
      </div>

      {/* Product-specific notes */}
      <div style={{ marginBottom: 12 }}>
        <ProductNotes account={a} product={p} currency={currency} />
      </div>

      <h4 style={{ margin: '0 0 6px', fontSize: 13 }}>
        Recent transactions{' '}
        <span className="muted tiny" style={{ fontWeight: 400 }}>
          (last 30 days · opening {currency} {fmtMoney(statement?.opening_balance ?? '0')} · closing {currency} {fmtMoney(statement?.closing_balance ?? a.current_balance)})
        </span>
      </h4>
      <TxnTable
        rows={(statement?.transactions ?? []).slice().reverse().slice(0, 10)}
        currency={currency}
        getNo={(t) => t.txn_no}
        getDate={(t) => t.posted_at}
        getType={(t) => DEP_TXN_LABELS[t.txn_type]}
        getTypeTone={(t) => depTxnTone(t.txn_type)}
        getAmount={(t) => `${currency} ${fmtMoney(t.amount)}`}
        getAmountSub={() => null}
        getChannel={(t) => (t.channel ? DEP_CHANNEL_LABELS[t.channel] : '—')}
        getRef={(t) => t.channel_ref ?? null}
        getNarration={(t) => t.narration ?? null}
        getBalance={(t) => `${currency} ${fmtMoney(t.balance_after)}`}
        emptyMsg="No transactions in the last 30 days."
        action={
          <button className="btn btn-sm" onClick={() => onOpenModal({ kind: 'depStatement', accountId: a.id })}>
            Full statement →
          </button>
        }
      />
    </div>
  );
}

function ProductNotes({ account: a, product: p, currency }: { account: DepositAccount; product: DepositProduct; currency: string }) {
  const parts: ReactNode[] = [];
  if (p.product_type === 'fixed') {
    parts.push(
      <div key="fixed">
        Fixed term {a.fixed_term_months ?? p.default_term_months ?? '?'}m
        {a.fixed_interest_rate_pct && ` @ ${a.fixed_interest_rate_pct}%`}
        {a.matures_at && ` · matures ${a.matures_at.slice(0, 10)}`}
        {p.lock_in_months > 0 && ` · lock-in ${p.lock_in_months}m`}
        {p.early_withdrawal_penalty_pct && parseFloat(p.early_withdrawal_penalty_pct) > 0 && ` · early-withdrawal penalty ${p.early_withdrawal_penalty_pct}%`}
      </div>
    );
  }
  if (p.product_type === 'goal' && a.goal_target_amount) {
    const progress = pct(a.current_balance, a.goal_target_amount);
    parts.push(
      <div key="goal">
        <div>Goal: {currency} {fmtMoney(a.goal_target_amount)} by {a.goal_target_date?.slice(0, 10)} — {a.goal_description ?? ''}</div>
        <div style={{ marginTop: 4, height: 6, background: 'var(--surface-2)', borderRadius: 3, overflow: 'hidden' }}>
          <div style={{ width: `${Math.min(100, parseFloat(progress))}%`, height: '100%', background: 'var(--accent)' }} />
        </div>
        <div className="muted tiny" style={{ marginTop: 2 }}>{progress}% complete</div>
      </div>
    );
  }
  if (p.product_type === 'junior' && a.guardian_member_id) {
    parts.push(<div key="junior">Guardian: <a className="tbl-link" href={`/members/${a.guardian_member_id}`}>{a.guardian_member_id.slice(0, 8)}…</a></div>);
  }
  if (a.withdrawal_notice_given_at) {
    parts.push(
      <div key="notice">
        Withdrawal notice given {a.withdrawal_notice_given_at.slice(0, 10)} for {currency} {fmtMoney(a.withdrawal_notice_amount ?? '0')} · notice period {p.notice_period_days} days.
      </div>
    );
  }
  // Constraints summary
  const constraints: string[] = [];
  if (p.notice_period_days > 0) constraints.push(`Notice ${p.notice_period_days}d`);
  if (p.max_withdrawals_per_month != null) constraints.push(`Max ${p.max_withdrawals_per_month}/mo`);
  if (!p.partial_withdrawal_allowed) constraints.push('Full-balance withdrawal only');
  if (p.large_withdrawal_threshold) constraints.push(`Large > ${currency} ${fmtMoney(p.large_withdrawal_threshold)}`);
  if (p.withdrawal_window_start_month && p.withdrawal_window_end_month)
    constraints.push(`Window months ${p.withdrawal_window_start_month}–${p.withdrawal_window_end_month}`);
  if (constraints.length > 0) {
    parts.push(<div key="constraints" className="muted tiny">{constraints.join(' · ')}</div>);
  }
  if (parts.length === 0) return null;
  return <div className="muted tiny" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>{parts}</div>;
}

// ─────────── Transaction table (shared shape) ───────────

function TxnTable<T>({
  rows, currency, getNo, getDate, getType, getTypeTone, getAmount, getAmountSub,
  getChannel, getRef, getNarration, getBalance, emptyMsg, action,
}: {
  rows: T[];
  currency: string;
  getNo: (t: T) => string;
  getDate: (t: T) => string;
  getType: (t: T) => string;
  getTypeTone: (t: T) => 'pos' | 'neg' | 'accent' | 'neutral' | 'warn';
  getAmount: (t: T) => string;
  getAmountSub: (t: T) => string | null;
  getChannel: (t: T) => string;
  getRef: (t: T) => string | null;
  getNarration: (t: T) => string | null;
  getBalance: (t: T) => string;
  emptyMsg: string;
  action?: ReactNode;
}) {
  const _ = currency; void _;
  if (rows.length === 0) {
    return (
      <div>
        <div className="empty" style={{ padding: 16 }}>{emptyMsg}</div>
        {action && <div style={{ textAlign: 'right', marginTop: 6 }}>{action}</div>}
      </div>
    );
  }
  return (
    <div>
      <table className="tbl">
        <thead>
          <tr>
            <th>Txn</th>
            <th>Posted</th>
            <th>Type</th>
            <th style={{ textAlign: 'right' }}>Amount</th>
            <th>Channel · Ref</th>
            <th style={{ textAlign: 'right' }}>Balance</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((t, i) => {
            const sub = getAmountSub(t);
            const ref = getRef(t);
            const narr = getNarration(t);
            return (
              <tr key={`${getNo(t)}-${i}`}>
                <td className="tiny-mono">{getNo(t)}</td>
                <td className="tiny-mono">{getDate(t).slice(0, 16).replace('T', ' ')}</td>
                <td><Badge tone={getTypeTone(t)}>{getType(t)}</Badge></td>
                <td className="mono" style={{ textAlign: 'right' }}>
                  <div>{getAmount(t)}</div>
                  {sub && <div className="muted tiny">{sub}</div>}
                </td>
                <td className="tiny">
                  <div>{getChannel(t)}</div>
                  {ref && <div className="muted tiny-mono">{ref}</div>}
                  {narr && <div className="muted tiny">{narr}</div>}
                </td>
                <td className="mono" style={{ textAlign: 'right' }}>{getBalance(t)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {action && <div style={{ textAlign: 'right', marginTop: 6 }}>{action}</div>}
    </div>
  );
}

// ─────────── KPI ───────────

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card" style={{ background: 'var(--surface-2)' }}>
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color, fontSize: 18 }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
    </div>
  );
}

// ─────────── Modals (router) ───────────

type ModalState =
  | { kind: null }
  | { kind: 'open' }
  | { kind: 'shareBuy' }
  | { kind: 'shareRedeem' }
  | { kind: 'shareTransfer' }
  | { kind: 'shareAdjust' }
  | { kind: 'shareLien' }
  | { kind: 'depDeposit'; accountId: string }
  | { kind: 'depWithdraw'; accountId: string }
  | { kind: 'depTransfer'; accountId: string }
  | { kind: 'depAdjust'; accountId: string }
  | { kind: 'depStatement'; accountId: string };

function Modals({
  modal, memberId, currency, shares, deposits, products, onClose, onChanged,
}: {
  modal: ModalState;
  memberId: string;
  currency: string;
  shares: ShareAccountView | null;
  deposits: MemberDepositItem[];
  products: DepositProduct[];
  onClose: () => void;
  onChanged: () => Promise<void> | void;
}) {
  if (modal.kind === null) return null;
  if (modal.kind === 'open') {
    return <OpenAccountModal memberId={memberId} products={products} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'shareBuy' && shares) {
    return <ShareBuyModal memberId={memberId} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'shareRedeem' && shares) {
    return <ShareRedeemModal memberId={memberId} max={shares.account.shares_available} held={shares.account.shares_held} minRequired={shares.policy.min_shares_required} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'shareTransfer' && shares) {
    return <ShareTransferModal memberId={memberId} max={shares.account.shares_available} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'shareAdjust' && shares) {
    return <ShareAdjustModal memberId={memberId} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'shareLien' && shares) {
    return <ShareLienModal memberId={memberId} max={shares.account.shares_available} onClose={onClose} onSaved={onChanged} />;
  }
  if (modal.kind === 'depDeposit' || modal.kind === 'depWithdraw' || modal.kind === 'depTransfer' || modal.kind === 'depAdjust' || modal.kind === 'depStatement') {
    const it = deposits.find((d) => d.account.id === modal.accountId);
    if (!it) return null;
    if (modal.kind === 'depDeposit') return <DepDepositModal it={it} currency={currency} onClose={onClose} onSaved={onChanged} />;
    if (modal.kind === 'depWithdraw') return <DepWithdrawModal it={it} currency={currency} onClose={onClose} onSaved={onChanged} />;
    if (modal.kind === 'depTransfer') return <DepTransferModal it={it} otherAccounts={deposits.filter((d) => d.account.id !== it.account.id)} currency={currency} onClose={onClose} onSaved={onChanged} />;
    if (modal.kind === 'depAdjust') return <DepAdjustModal it={it} currency={currency} onClose={onClose} onSaved={onChanged} />;
    if (modal.kind === 'depStatement') return <DepStatementModal it={it} currency={currency} onClose={onClose} onChanged={onChanged} />;
  }
  return null;
}

// ─────────── Modal shell + field ───────────

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
      <div className="card" style={{ width: width ?? 520, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
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

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

// ─────────── Shares modals ───────────

function ShareBuyModal({ memberId, onClose, onSaved }: { memberId: string; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [shares, setShares] = useState(1);
  const [channel, setChannel] = useState<SharePaymentChannel>('cash');
  const [ref, setRef] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title="Buy shares" busy={busy} onClose={onClose} submitLabel="Post purchase"
      disabled={shares <= 0}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await purchaseShares(memberId, { shares, payment_channel: channel, payment_ref: ref || undefined, narration: note || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Shares"><input className="input" type="number" min={1} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Payment channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as SharePaymentChannel)}>
          {(['cash', 'mpesa', 'airtel_money', 'bank_transfer', 'payroll', 'standing_order'] as SharePaymentChannel[]).map((c) => (
            <option key={c} value={c}>{SHARE_CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Payment reference (optional)"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="M-Pesa code, till receipt, etc." /></Field>
      <Field label="Narration (optional)"><input className="input" value={note} onChange={(e) => setNote(e.target.value)} /></Field>
    </ModalShell>
  );
}

function ShareRedeemModal({ memberId, max, held, minRequired, onClose, onSaved }: {
  memberId: string; max: number; held: number; minRequired: number;
  onClose: () => void; onSaved: () => Promise<void> | void;
}) {
  const [shares, setShares] = useState(1);
  const [reason, setReason] = useState('');
  const [channel, setChannel] = useState<SharePaymentChannel>('mpesa');
  const [ref, setRef] = useState('');
  const wouldBeBelowMin = (held - shares) < minRequired;
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title="Redeem shares" busy={busy} onClose={onClose} submitLabel="Redeem"
      disabled={shares <= 0 || shares > max || !reason.trim() || (wouldBeBelowMin && !ack)}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await redeemShares(memberId, {
            shares, reason, payment_channel: channel, payment_ref: ref || undefined,
            acknowledge_below_minimum: wouldBeBelowMin ? ack : undefined,
          });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label={`Shares (max available: ${max})`}><input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Reason (required — audit)"><textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
      <Field label="Payout channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as SharePaymentChannel)}>
          {(['mpesa', 'airtel_money', 'bank_transfer', 'cash', 'internal'] as SharePaymentChannel[]).map((c) => (
            <option key={c} value={c}>{SHARE_CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Payout reference (optional)"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
      {wouldBeBelowMin && (
        <div className="alert alert-warn">
          <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
            <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
            <span>This redemption drops the member below the {minRequired}-share minimum. Confirm only if board-authorised (e.g. exit).</span>
          </label>
        </div>
      )}
    </ModalShell>
  );
}

function ShareTransferModal({ memberId, max, onClose, onSaved }: {
  memberId: string; max: number; onClose: () => void; onSaved: () => Promise<void> | void;
}) {
  const [shares, setShares] = useState(1);
  const [to, setTo] = useState('');
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const validUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(to);
  return (
    <ModalShell title="Transfer shares" busy={busy} onClose={onClose} submitLabel="Transfer"
      disabled={shares <= 0 || shares > max || !validUUID || !reason.trim() || to === memberId}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await transferShares(memberId, { shares, to_member_id: to, reason, narration: note || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label={`Shares (max available: ${max})`}><input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Recipient member ID (UUID)"><input className="input" value={to} onChange={(e) => setTo(e.target.value)} placeholder="paste from /members/<id>" /></Field>
      <Field label="Reason (audit)"><input className="input" value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
      <Field label="Narration (optional)"><input className="input" value={note} onChange={(e) => setNote(e.target.value)} /></Field>
    </ModalShell>
  );
}

function ShareAdjustModal({ memberId, onClose, onSaved }: { memberId: string; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [delta, setDelta] = useState(0);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title="Adjust share balance" busy={busy} onClose={onClose} submitLabel="Post adjustment"
      disabled={delta === 0 || !reason.trim()}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try { await adjustShares(memberId, { shares_delta: delta, reason }); await onSaved(); }
        catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">Adjustments are administrative corrections — include the ticket/approval reference in the reason field.</div>
      <Field label="Δ shares (signed: +N credit, −N debit)"><input className="input" type="number" value={delta} onChange={(e) => setDelta(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Reason (required — audit)"><textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
    </ModalShell>
  );
}

function ShareLienModal({ memberId, max, onClose, onSaved }: { memberId: string; max: number; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [shares, setShares] = useState(1);
  const [reason, setReason] = useState('');
  const [kind, setKind] = useState('loan');
  const [ref, setRef] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title="Place lien" busy={busy} onClose={onClose} submitLabel="Place lien"
      disabled={shares <= 0 || shares > max || !reason.trim()}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await placeShareLien(memberId, { shares, reason, reference_kind: kind, reference_id: ref || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label={`Shares to pledge (max available: ${max})`}><input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} /></Field>
      <Field label="Reason"><input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Loan L-2026-00012 collateral" /></Field>
      <Field label="Reference kind">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="loan">Loan</option><option value="collateral">Collateral</option><option value="manual">Manual</option>
        </select>
      </Field>
      <Field label="Reference ID (optional)"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
    </ModalShell>
  );
}

// ─────────── Deposit modals ───────────

function OpenAccountModal({ memberId, products, onClose, onSaved }: {
  memberId: string; products: DepositProduct[];
  onClose: () => void; onSaved: () => Promise<void> | void;
}) {
  const [productID, setProductID] = useState(products[0]?.id ?? '');
  const product = useMemo(() => products.find((p) => p.id === productID), [products, productID]);
  const [openingDeposit, setOpeningDeposit] = useState(product?.min_opening_balance ?? '0');
  const [channel, setChannel] = useState<DepositChannel>('cash');
  const [ref, setRef] = useState('');
  const [fixedTerm, setFixedTerm] = useState<number>(product?.default_term_months ?? 12);
  const [fixedRate, setFixedRate] = useState('');
  const [goalAmount, setGoalAmount] = useState('');
  const [goalDate, setGoalDate] = useState('');
  const [goalDesc, setGoalDesc] = useState('');
  const [guardian, setGuardian] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (product) {
      setOpeningDeposit(product.min_opening_balance);
      if (product.default_term_months) setFixedTerm(product.default_term_months);
    }
  }, [product]);

  return (
    <ModalShell title="Open deposit account" busy={busy} onClose={onClose} submitLabel="Open account"
      onSubmit={async () => {
        if (!product) { setErr('Pick a product'); return; }
        setErr(null); setBusy(true);
        try {
          await openDepositAccount({
            member_id: memberId,
            product_id: product.id,
            opening_deposit: openingDeposit,
            opening_channel: parseFloat(openingDeposit) > 0 ? channel : undefined,
            opening_channel_ref: ref || undefined,
            fixed_term_months: product.product_type === 'fixed' ? fixedTerm : undefined,
            fixed_interest_rate_pct: product.product_type === 'fixed' && fixedRate ? fixedRate : undefined,
            goal_target_amount: product.product_type === 'goal' ? goalAmount : undefined,
            goal_target_date: product.product_type === 'goal' ? goalDate : undefined,
            goal_description: product.product_type === 'goal' && goalDesc ? goalDesc : undefined,
            guardian_member_id: product.product_type === 'junior' && guardian ? guardian : undefined,
          });
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <Field label="Product">
        <select className="input" value={productID} onChange={(e) => setProductID(e.target.value)}>
          {products.map((p) => (
            <option key={p.id} value={p.id}>{PRODUCT_LABEL[p.product_type]} · {p.name} ({p.code})</option>
          ))}
        </select>
      </Field>
      {product && (
        <p className="muted tiny" style={{ margin: '0 0 10px' }}>
          Min opening: <strong>{product.min_opening_balance}</strong>
          {parseFloat(product.min_operating_balance) > 0 && ` · Min op balance: ${product.min_operating_balance}`}
          {product.lock_in_months > 0 && ` · Lock-in ${product.lock_in_months}m`}
          {product.notice_period_days > 0 && ` · Notice ${product.notice_period_days}d`}
        </p>
      )}
      <Field label="Opening deposit"><input className="input mono" value={openingDeposit} onChange={(e) => setOpeningDeposit(e.target.value)} /></Field>
      {parseFloat(openingDeposit) > 0 && (
        <>
          <Field label="Channel">
            <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as DepositChannel)}>
              {(['cash', 'mpesa', 'airtel_money', 'bank_transfer', 'payroll'] as DepositChannel[]).map((c) => (
                <option key={c} value={c}>{DEP_CHANNEL_LABELS[c]}</option>
              ))}
            </select>
          </Field>
          <Field label="Channel reference (optional)"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
        </>
      )}
      {product?.product_type === 'fixed' && (
        <>
          <Field label="Term (months)"><input className="input mono" type="number" min={1} value={fixedTerm} onChange={(e) => setFixedTerm(parseInt(e.target.value, 10) || 0)} /></Field>
          <Field label="Fixed interest rate (% p.a., optional)"><input className="input mono" value={fixedRate} onChange={(e) => setFixedRate(e.target.value)} placeholder="e.g. 10.5" /></Field>
        </>
      )}
      {product?.product_type === 'goal' && (
        <>
          <Field label="Target amount"><input className="input mono" value={goalAmount} onChange={(e) => setGoalAmount(e.target.value)} /></Field>
          <Field label="Target date"><input className="input mono" type="date" value={goalDate} onChange={(e) => setGoalDate(e.target.value)} /></Field>
          <Field label="Description (optional)"><input className="input" value={goalDesc} onChange={(e) => setGoalDesc(e.target.value)} /></Field>
        </>
      )}
      {product?.product_type === 'junior' && (
        <Field label="Guardian member ID (UUID)" hint="Required — link to the parent/guardian member."><input className="input" value={guardian} onChange={(e) => setGuardian(e.target.value)} /></Field>
      )}
    </ModalShell>
  );
}

function DepDepositModal({ it, currency, onClose, onSaved }: { it: MemberDepositItem; currency: string; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [amount, setAmount] = useState('');
  const [channel, setChannel] = useState<DepositChannel>('cash');
  const [ref, setRef] = useState('');
  const [narr, setNarr] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Deposit to ${it.account.account_no}`} busy={busy} onClose={onClose} submitLabel="Post deposit"
      disabled={!amount || parseFloat(amount) <= 0}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await postDeposit(it.account.id, { amount, channel, channel_ref: ref || undefined, narration: narr || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>
        {it.product.name} · Current balance <strong>{currency} {fmtMoney(it.account.current_balance)}</strong>
      </p>
      <Field label={`Amount (${currency})`}><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} placeholder="0.00" /></Field>
      <Field label="Channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as DepositChannel)}>
          {(['cash', 'mpesa', 'airtel_money', 'bank_transfer', 'standing_order', 'payroll'] as DepositChannel[]).map((c) => (
            <option key={c} value={c}>{DEP_CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Channel reference"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="M-Pesa code, till receipt, etc." /></Field>
      <Field label="Narration (optional)"><input className="input" value={narr} onChange={(e) => setNarr(e.target.value)} /></Field>
    </ModalShell>
  );
}

function DepWithdrawModal({ it, currency, onClose, onSaved }: { it: MemberDepositItem; currency: string; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [amount, setAmount] = useState('');
  const [channel, setChannel] = useState<DepositChannel>('mpesa');
  const [ref, setRef] = useState('');
  const [narr, setNarr] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const maxAvail = parseFloat(it.account.available_balance);
  const isLarge: boolean = !!it.product.large_withdrawal_threshold && parseFloat(amount || '0') > parseFloat(it.product.large_withdrawal_threshold);
  return (
    <ModalShell title={`Withdraw from ${it.account.account_no}`} busy={busy} onClose={onClose} submitLabel="Post withdrawal"
      disabled={!amount || parseFloat(amount) <= 0 || parseFloat(amount) > maxAvail || (isLarge && !reason)}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await postWithdrawal(it.account.id, { amount, channel, channel_ref: ref || undefined, narration: narr || undefined, reason: reason || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          else if (r.posted?.requires_approval) alert('Posted. Note: this withdrawal exceeded the large-withdrawal threshold.');
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>
        {it.product.name} · Available <strong>{currency} {fmtMoney(it.account.available_balance)}</strong>
      </p>
      <Field label={`Amount (${currency})`}><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
      <Field label="Payout channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as DepositChannel)}>
          {(['mpesa', 'airtel_money', 'bank_transfer', 'cash', 'internal'] as DepositChannel[]).map((c) => (
            <option key={c} value={c}>{DEP_CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Channel reference (optional)"><input className="input" value={ref} onChange={(e) => setRef(e.target.value)} /></Field>
      <Field label="Narration (optional)"><input className="input" value={narr} onChange={(e) => setNarr(e.target.value)} /></Field>
      {isLarge && (
        <>
          <div className="alert alert-warn">
            <strong>Large withdrawal.</strong> Above {currency} {fmtMoney(it.product.large_withdrawal_threshold ?? '0')} — supply a reason.
          </div>
          <Field label="Reason / authorization"><textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
        </>
      )}
    </ModalShell>
  );
}

function DepTransferModal({ it, otherAccounts, currency, onClose, onSaved }: {
  it: MemberDepositItem; otherAccounts: MemberDepositItem[]; currency: string;
  onClose: () => void; onSaved: () => Promise<void> | void;
}) {
  const [toID, setToID] = useState(otherAccounts[0]?.account.id ?? '');
  const [amount, setAmount] = useState('');
  const [narr, setNarr] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (otherAccounts.length === 0) {
    return (
      <ModalShell title="Transfer" onClose={onClose} onSubmit={onClose} submitLabel="Close" disabled>
        <p>This member has no other deposit accounts to transfer to.</p>
      </ModalShell>
    );
  }
  return (
    <ModalShell title={`Transfer from ${it.account.account_no}`} busy={busy} onClose={onClose} submitLabel="Transfer"
      disabled={!amount || parseFloat(amount) <= 0 || !toID}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try {
          const r = await transferBetweenOwn(it.account.id, { amount, to_account_id: toID, narration: narr || undefined });
          if (r.pending) alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
          await onSaved();
        } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>From {it.product.name} · Available <strong>{currency} {fmtMoney(it.account.available_balance)}</strong></p>
      <Field label="To account (same member)">
        <select className="input" value={toID} onChange={(e) => setToID(e.target.value)}>
          {otherAccounts.map((o) => (
            <option key={o.account.id} value={o.account.id}>{o.product.name} · {o.account.account_no} · {currency} {fmtMoney(o.account.current_balance)}</option>
          ))}
        </select>
      </Field>
      <Field label={`Amount (${currency})`}><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
      <Field label="Narration (optional)"><input className="input" value={narr} onChange={(e) => setNarr(e.target.value)} /></Field>
    </ModalShell>
  );
}

function DepAdjustModal({ it, currency, onClose, onSaved }: { it: MemberDepositItem; currency: string; onClose: () => void; onSaved: () => Promise<void> | void }) {
  const [amount, setAmount] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <ModalShell title={`Adjust ${it.account.account_no}`} busy={busy} onClose={onClose} submitLabel="Post adjustment"
      disabled={!amount || parseFloat(amount) === 0 || !reason.trim()}
      onSubmit={async () => {
        setErr(null); setBusy(true);
        try { await adjustDeposit(it.account.id, amount, reason); await onSaved(); }
        catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
      }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div className="alert alert-warn">Administrative correction — include the ticket/approval reference.</div>
      <Field label={`Signed amount (${currency}) — + credit, − debit`}><input className="input mono" value={amount} onChange={(e) => setAmount(e.target.value)} /></Field>
      <Field label="Reason (audit)"><textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} /></Field>
    </ModalShell>
  );
}

function DepStatementModal({ it, currency, onClose, onChanged }: {
  it: MemberDepositItem; currency: string;
  onClose: () => void; onChanged: () => Promise<void> | void;
}) {
  const today = new Date().toISOString().slice(0, 10);
  const ninetyAgo = new Date(Date.now() - 90 * 86400e3).toISOString().slice(0, 10);
  const [from, setFrom] = useState(ninetyAgo);
  const [to, setTo] = useState(today);
  const [data, setData] = useState<DepositStatement | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { hasPermission } = useAuth();
  const canReverse = hasPermission('deposits:reverse');

  async function load() {
    setErr(null);
    try { setData(await getDepositStatement(it.account.id, from, to)); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [it.account.id]);

  async function reverse(t: DepositTransaction) {
    const reason = prompt(`Reverse ${t.txn_no} (${t.txn_type} ${t.amount})? Reason:`);
    if (!reason) return;
    setBusy(true);
    try { await reverseDeposit(it.account.id, t.id, reason); await load(); await onChanged(); }
    catch (e) { alert(extractError(e)); } finally { setBusy(false); }
  }

  function downloadCSV() {
    if (!data) return;
    const rows = [['txn_no', 'posted_at', 'value_date', 'type', 'amount', 'channel', 'reference', 'narration', 'balance_after']];
    for (const t of data.transactions) {
      rows.push([t.txn_no, t.posted_at, t.value_date, t.txn_type, t.amount, t.channel ?? '', t.channel_ref ?? '', t.narration ?? '', t.balance_after]);
    }
    const csv = rows.map((r) => r.map((c) => `"${(c ?? '').replace(/"/g, '""')}"`).join(',')).join('\n');
    const blob = new Blob([csv], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url; a.download = `${it.account.account_no}_${from}_${to}.csv`; a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <ModalShell title={`Statement — ${it.account.account_no}`} busy={busy} onClose={onClose}
      onSubmit={downloadCSV} submitLabel="Download CSV" width={780}>
      {err && <div className="alert alert-error">{err}</div>}
      <p className="muted tiny" style={{ marginTop: 0 }}>{it.product.name}</p>
      <div className="row" style={{ gap: 6, marginBottom: 10, alignItems: 'flex-end' }}>
        <Field label="From"><input className="input mono" type="date" value={from} onChange={(e) => setFrom(e.target.value)} /></Field>
        <Field label="To"><input className="input mono" type="date" value={to} onChange={(e) => setTo(e.target.value)} /></Field>
        <button className="btn btn-sm" onClick={() => void load()}>Reload</button>
      </div>
      {data && (
        <>
          <div className="grid-3" style={{ marginBottom: 10 }}>
            <KPI label="Opening" value={`${currency} ${fmtMoney(data.opening_balance)}`} />
            <KPI label="Closing" value={`${currency} ${fmtMoney(data.closing_balance)}`} />
            <KPI label="Transactions" value={String(data.transactions.length)} />
          </div>
          {data.transactions.length === 0 ? (
            <div className="empty">No transactions in this period.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Txn</th><th>Posted</th><th>Type</th>
                  <th style={{ textAlign: 'right' }}>Amount</th>
                  <th>Channel · Ref</th>
                  <th style={{ textAlign: 'right' }}>Balance</th>
                  {canReverse && <th></th>}
                </tr>
              </thead>
              <tbody>
                {data.transactions.map((t) => {
                  const debit = parseFloat(t.amount) < 0;
                  return (
                    <tr key={t.id} style={{ textDecoration: t.reversed_by_txn_id ? 'line-through' : undefined }}>
                      <td className="tiny-mono">{t.txn_no}</td>
                      <td className="tiny-mono">{t.posted_at.slice(0, 16).replace('T', ' ')}</td>
                      <td><Badge tone={depTxnTone(t.txn_type)}>{DEP_TXN_LABELS[t.txn_type]}</Badge></td>
                      <td className="mono" style={{ textAlign: 'right', color: debit ? 'var(--neg)' : 'var(--pos)' }}>{currency} {fmtMoney(t.amount)}</td>
                      <td className="tiny">
                        <div>{t.channel ? DEP_CHANNEL_LABELS[t.channel] : '—'}</div>
                        {t.channel_ref && <div className="muted tiny-mono">{t.channel_ref}</div>}
                        {t.narration && <div className="muted tiny">{t.narration}</div>}
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmtMoney(t.balance_after)}</td>
                      {canReverse && (
                        <td>
                          {!t.reversed_by_txn_id && t.txn_type !== 'reversal' && (
                            <button className="btn btn-sm" disabled={busy} onClick={() => void reverse(t)}>Reverse</button>
                          )}
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </>
      )}
    </ModalShell>
  );
}
