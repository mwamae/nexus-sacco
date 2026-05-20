// Member share account UI: balance + lien display, transaction history,
// and buy/redeem/transfer/adjust/lien modals. Drops into the Accounts
// tab on MemberProfile and replaces the legacy "Shares" pending card.

import { useEffect, useState } from 'react';
import {
  adjustShares,
  extractError,
  getCurrentCertificate,
  getShareAccountByMember,
  listShareTransactions,
  placeShareLien,
  purchaseShares,
  redeemShares,
  releaseShareLien,
  transferShares,
  type ShareAccountView,
  type ShareCertificate,
  type SharePaymentChannel,
  type ShareTransaction,
  type ShareTxnType,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Badge } from './Badge';
import { Icon } from './Icon';

const CHANNEL_LABELS: Record<SharePaymentChannel, string> = {
  cash: 'Cash',
  mpesa: 'M-Pesa',
  airtel_money: 'Airtel Money',
  bank_transfer: 'Bank transfer',
  payroll: 'Payroll',
  standing_order: 'Standing order',
  internal: 'Internal',
};

const TXN_LABELS: Record<ShareTxnType, string> = {
  purchase: 'Purchase',
  transfer_in: 'Transfer in',
  transfer_out: 'Transfer out',
  redemption: 'Redemption',
  adjustment: 'Adjustment',
  bonus_issue: 'Bonus issue',
};

type Modal = null | 'buy' | 'redeem' | 'transfer' | 'adjust' | 'lien';

export function MemberSharesCard({ memberId, currency }: { memberId: string; currency: string }) {
  const { hasPermission } = useAuth();
  const [view, setView] = useState<ShareAccountView | null>(null);
  const [txns, setTxns] = useState<ShareTransaction[]>([]);
  const [cert, setCert] = useState<ShareCertificate | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [modal, setModal] = useState<Modal>(null);
  const [busy, setBusy] = useState(false);

  const canBuy = hasPermission('shares:buy');
  const canRedeem = hasPermission('shares:redeem');
  const canTransfer = hasPermission('shares:transfer');
  const canAdjust = hasPermission('shares:adjust');
  const canLien = hasPermission('shares:lien');

  async function reload() {
    setErr(null);
    try {
      const [v, t, c] = await Promise.all([
        getShareAccountByMember(memberId),
        listShareTransactions(memberId, { limit: 25 }),
        getCurrentCertificate(memberId),
      ]);
      setView(v);
      setTxns(t);
      setCert(c);
    } catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [memberId]);

  if (err) {
    return (
      <div className="card">
        <div className="card-hd"><h3>Shares</h3></div>
        <div className="card-body"><div className="alert alert-error">{err}</div></div>
      </div>
    );
  }
  if (!view) {
    return (
      <div className="card">
        <div className="card-hd"><h3>Shares</h3></div>
        <div className="card-body"><div className="empty">Loading…</div></div>
      </div>
    );
  }

  const a = view.account;
  const policy = view.policy;
  const minRequired = policy.min_shares_required;
  const belowMin = a.shares_held < minRequired;
  const hasLien = a.shares_pledged > 0;

  return (
    <>
      <div className="card">
        <div className="card-hd">
          <h3>Shares</h3>
          <span className="card-sub">Equity capital · account {a.account_no}</span>
          <div className="card-hd-actions">
            {canBuy && <button className="btn btn-sm btn-accent" onClick={() => setModal('buy')}><Icon name="plus" size={12} /> Buy</button>}
            {canRedeem && <button className="btn btn-sm" disabled={a.shares_held === 0} onClick={() => setModal('redeem')}>Redeem</button>}
            {canTransfer && <button className="btn btn-sm" disabled={a.shares_available === 0} onClick={() => setModal('transfer')}>Transfer</button>}
            {canAdjust && <button className="btn btn-sm" onClick={() => setModal('adjust')}>Adjust</button>}
          </div>
        </div>
        <div className="card-body">
          <div className="grid-3" style={{ marginBottom: 12 }}>
            <KPI label="Shares held" value={String(a.shares_held)} sub={`min ${minRequired}`} tone={belowMin ? 'warn' : 'pos'} />
            <KPI label="Capital value" value={`${currency} ${fmtMoney(a.total_value)}`} sub={`@ ${currency} ${policy.par_value}/share`} />
            <KPI
              label="Available"
              value={String(a.shares_available)}
              sub={hasLien ? `${a.shares_pledged} pledged` : 'no lien'}
              tone={hasLien ? 'warn' : undefined}
            />
          </div>

          {belowMin && (
            <div className="alert alert-warn" style={{ marginBottom: 12 }}>
              Member is below the {minRequired}-share minimum. They are not in good standing for member benefits.
            </div>
          )}

          {view.active_liens.length > 0 && (
            <div className="card" style={{ marginBottom: 12, background: 'var(--surface-2)' }}>
              <div className="card-hd"><h4 style={{ margin: 0, fontSize: 13 }}>Active liens</h4></div>
              <table className="tbl">
                <thead><tr><th>Shares</th><th>Reason</th><th>Placed</th><th></th></tr></thead>
                <tbody>
                  {view.active_liens.map((l) => (
                    <tr key={l.id}>
                      <td className="mono">{l.shares_pledged}</td>
                      <td>
                        <div>{l.reason}</div>
                        {l.reference_kind && <div className="muted tiny">{l.reference_kind}: {l.reference_id}</div>}
                      </td>
                      <td className="tiny-mono">{new Date(l.placed_at).toISOString().slice(0, 10)}</td>
                      <td>
                        {canLien && (
                          <button
                            className="btn btn-sm"
                            onClick={async () => {
                              const reason = prompt('Release reason?'); if (!reason) return;
                              try { await releaseShareLien(l.id, reason); await reload(); }
                              catch (e) { alert(extractError(e)); }
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

          {canLien && (
            <button className="btn btn-sm" onClick={() => setModal('lien')}>+ Place lien</button>
          )}

          {cert && (
            <p className="muted tiny" style={{ marginTop: 12 }}>
              Current share certificate: <strong className="mono">{cert.certificate_no}</strong>
              {' '}({cert.shares_covered} shares, {currency} {fmtMoney(cert.total_value)})
              {' '}— issued {new Date(cert.issued_at).toISOString().slice(0, 10)}
            </p>
          )}
        </div>
      </div>

      {/* Transaction history */}
      <div className="card" style={{ marginTop: 14 }}>
        <div className="card-hd">
          <h3>Share transactions</h3>
          <span className="card-sub">{txns.length} most recent</span>
        </div>
        <div className="card-body flush">
          {txns.length === 0 ? (
            <div className="empty">No share transactions yet.</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Txn</th>
                  <th>Type</th>
                  <th style={{ textAlign: 'right' }}>Δ shares</th>
                  <th style={{ textAlign: 'right' }}>Amount</th>
                  <th style={{ textAlign: 'right' }}>Balance</th>
                  <th>Channel</th>
                  <th>Posted</th>
                </tr>
              </thead>
              <tbody>
                {txns.map((t) => (
                  <tr key={t.id}>
                    <td className="tiny-mono">{t.txn_no}</td>
                    <td><Badge tone={txnTone(t.txn_type)}>{TXN_LABELS[t.txn_type]}</Badge></td>
                    <td className="mono" style={{ textAlign: 'right', color: t.shares_delta < 0 ? 'var(--neg)' : 'var(--pos)' }}>
                      {t.shares_delta > 0 ? `+${t.shares_delta}` : t.shares_delta}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmtMoney(t.amount)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{t.balance_after_shares}</td>
                    <td className="tiny">{t.payment_channel ? CHANNEL_LABELS[t.payment_channel] : '—'}</td>
                    <td className="tiny-mono">{new Date(t.posted_at).toISOString().slice(0, 16).replace('T', ' ')}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {modal === 'buy' && (
        <BuyModal busy={busy} onClose={() => setModal(null)} onSubmit={async (in_) => {
          setBusy(true);
          try { await purchaseShares(memberId, in_); await reload(); setModal(null); }
          catch (e) { alert(extractError(e)); }
          finally { setBusy(false); }
        }} />
      )}
      {modal === 'redeem' && (
        <RedeemModal busy={busy} max={a.shares_available} minRequired={minRequired} held={a.shares_held}
          onClose={() => setModal(null)}
          onSubmit={async (in_) => {
            setBusy(true);
            try { await redeemShares(memberId, in_); await reload(); setModal(null); }
            catch (e) { alert(extractError(e)); }
            finally { setBusy(false); }
          }} />
      )}
      {modal === 'transfer' && (
        <TransferModal busy={busy} max={a.shares_available} thisMemberId={memberId}
          onClose={() => setModal(null)}
          onSubmit={async (in_) => {
            setBusy(true);
            try { await transferShares(memberId, in_); await reload(); setModal(null); }
            catch (e) { alert(extractError(e)); }
            finally { setBusy(false); }
          }} />
      )}
      {modal === 'adjust' && (
        <AdjustModal busy={busy} onClose={() => setModal(null)} onSubmit={async (in_) => {
          setBusy(true);
          try { await adjustShares(memberId, in_); await reload(); setModal(null); }
          catch (e) { alert(extractError(e)); }
          finally { setBusy(false); }
        }} />
      )}
      {modal === 'lien' && (
        <LienModal busy={busy} max={a.shares_available} onClose={() => setModal(null)} onSubmit={async (in_) => {
          setBusy(true);
          try { await placeShareLien(memberId, in_); await reload(); setModal(null); }
          catch (e) { alert(extractError(e)); }
          finally { setBusy(false); }
        }} />
      )}
    </>
  );
}

// ─────────── KPI atom ───────────

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card" style={{ background: 'var(--surface-2)' }}>
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
    </div>
  );
}

// ─────────── Modals ───────────

function ModalShell({ title, busy, onClose, children, submitLabel, onSubmit, disabled }: {
  title: string; busy: boolean; onClose: () => void; children: React.ReactNode;
  submitLabel: string; onSubmit: () => void; disabled?: boolean;
}) {
  return (
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 1000,
        background: 'rgba(0,0,0,.45)',
        display: 'grid', placeItems: 'center',
      }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 520, maxWidth: '90vw' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || disabled} onClick={onSubmit}>
            {busy ? 'Working…' : submitLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

function BuyModal({ busy, onClose, onSubmit }: {
  busy: boolean; onClose: () => void;
  onSubmit: (in_: { shares: number; payment_channel: SharePaymentChannel; payment_ref?: string; narration?: string }) => void;
}) {
  const [shares, setShares] = useState<number>(1);
  const [channel, setChannel] = useState<SharePaymentChannel>('cash');
  const [ref, setRef] = useState('');
  const [note, setNote] = useState('');
  return (
    <ModalShell title="Buy shares" busy={busy} onClose={onClose} submitLabel="Post purchase"
      disabled={shares <= 0}
      onSubmit={() => onSubmit({ shares, payment_channel: channel, payment_ref: ref || undefined, narration: note || undefined })}>
      <Field label="Shares">
        <input className="input" type="number" min={1} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Payment channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as SharePaymentChannel)}>
          {(['cash', 'mpesa', 'airtel_money', 'bank_transfer', 'payroll', 'standing_order'] as SharePaymentChannel[]).map((c) => (
            <option key={c} value={c}>{CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Payment reference (optional)">
        <input className="input" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="M-Pesa code, till receipt, etc." />
      </Field>
      <Field label="Narration (optional)">
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
    </ModalShell>
  );
}

function RedeemModal({ busy, max, held, minRequired, onClose, onSubmit }: {
  busy: boolean; max: number; held: number; minRequired: number; onClose: () => void;
  onSubmit: (in_: { shares: number; reason: string; payment_channel?: SharePaymentChannel; payment_ref?: string; acknowledge_below_minimum?: boolean }) => void;
}) {
  const [shares, setShares] = useState<number>(1);
  const [reason, setReason] = useState('');
  const [channel, setChannel] = useState<SharePaymentChannel>('mpesa');
  const [ref, setRef] = useState('');
  const wouldBeBelowMin = (held - shares) < minRequired;
  const [ack, setAck] = useState(false);
  return (
    <ModalShell title="Redeem shares" busy={busy} onClose={onClose} submitLabel="Redeem"
      disabled={shares <= 0 || shares > max || reason.trim() === '' || (wouldBeBelowMin && !ack)}
      onSubmit={() => onSubmit({
        shares, reason, payment_channel: channel, payment_ref: ref || undefined,
        acknowledge_below_minimum: wouldBeBelowMin ? ack : undefined,
      })}>
      <Field label={`Shares (max available: ${max})`}>
        <input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Reason (required — audit)">
        <textarea className="input" rows={2} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Member exit, hardship withdrawal, board directive…" />
      </Field>
      <Field label="Payout channel">
        <select className="input" value={channel} onChange={(e) => setChannel(e.target.value as SharePaymentChannel)}>
          {(['mpesa', 'airtel_money', 'bank_transfer', 'cash', 'internal'] as SharePaymentChannel[]).map((c) => (
            <option key={c} value={c}>{CHANNEL_LABELS[c]}</option>
          ))}
        </select>
      </Field>
      <Field label="Payout reference (optional)">
        <input className="input" value={ref} onChange={(e) => setRef(e.target.value)} />
      </Field>
      {wouldBeBelowMin && (
        <div className="alert alert-warn">
          <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
            <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
            <span>
              This redemption drops the member below the {minRequired}-share minimum.
              Confirm only if this is an exit redemption authorized by the board.
            </span>
          </label>
        </div>
      )}
    </ModalShell>
  );
}

function TransferModal({ busy, max, thisMemberId, onClose, onSubmit }: {
  busy: boolean; max: number; thisMemberId: string; onClose: () => void;
  onSubmit: (in_: { shares: number; to_member_id: string; reason: string; narration?: string }) => void;
}) {
  const [shares, setShares] = useState<number>(1);
  const [to, setTo] = useState('');
  const [reason, setReason] = useState('');
  const [note, setNote] = useState('');
  const validUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(to);
  return (
    <ModalShell title="Transfer shares" busy={busy} onClose={onClose} submitLabel="Transfer"
      disabled={shares <= 0 || shares > max || !validUUID || reason.trim() === '' || to === thisMemberId}
      onSubmit={() => onSubmit({ shares, to_member_id: to, reason, narration: note || undefined })}>
      <Field label={`Shares (max available: ${max})`}>
        <input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Recipient member ID (UUID)">
        <input className="input" value={to} onChange={(e) => setTo(e.target.value)} placeholder="paste from /members/<id>" />
      </Field>
      <Field label="Reason (audit)">
        <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Inter-member share transfer agreement" />
      </Field>
      <Field label="Narration (optional)">
        <input className="input" value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
    </ModalShell>
  );
}

function AdjustModal({ busy, onClose, onSubmit }: {
  busy: boolean; onClose: () => void;
  onSubmit: (in_: { shares_delta: number; reason: string }) => void;
}) {
  const [delta, setDelta] = useState<number>(0);
  const [reason, setReason] = useState('');
  return (
    <ModalShell title="Adjust share balance" busy={busy} onClose={onClose} submitLabel="Post adjustment"
      disabled={delta === 0 || reason.trim() === ''}
      onSubmit={() => onSubmit({ shares_delta: delta, reason })}>
      <div className="alert alert-warn">
        Adjustments are administrative corrections only — use with caution. They require a reason
        and are logged with dual authorization (initiator + your own user id).
      </div>
      <Field label="Δ shares (signed: +N to credit, -N to debit)">
        <input className="input" type="number" value={delta} onChange={(e) => setDelta(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Reason (required — audit)">
        <textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Reconciliation, data fix, etc. Include ticket / approval reference." />
      </Field>
    </ModalShell>
  );
}

function LienModal({ busy, max, onClose, onSubmit }: {
  busy: boolean; max: number; onClose: () => void;
  onSubmit: (in_: { shares: number; reason: string; reference_kind?: string; reference_id?: string }) => void;
}) {
  const [shares, setShares] = useState<number>(1);
  const [reason, setReason] = useState('');
  const [kind, setKind] = useState('loan');
  const [ref, setRef] = useState('');
  return (
    <ModalShell title="Place lien" busy={busy} onClose={onClose} submitLabel="Place lien"
      disabled={shares <= 0 || shares > max || reason.trim() === ''}
      onSubmit={() => onSubmit({ shares, reason, reference_kind: kind, reference_id: ref || undefined })}>
      <Field label={`Shares to pledge (max available: ${max})`}>
        <input className="input" type="number" min={1} max={max} value={shares} onChange={(e) => setShares(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Reason">
        <input className="input" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Loan L-2026-00012 collateral" />
      </Field>
      <Field label="Reference kind">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="loan">Loan</option>
          <option value="collateral">Collateral</option>
          <option value="manual">Manual</option>
        </select>
      </Field>
      <Field label="Reference ID (optional)">
        <input className="input" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="Loan number, court order ref, etc." />
      </Field>
    </ModalShell>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

function fmtMoney(s: string): string {
  const n = parseFloat(s);
  if (!isFinite(n)) return s;
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function txnTone(t: ShareTxnType): 'pos' | 'neg' | 'accent' | 'neutral' {
  switch (t) {
    case 'purchase': case 'transfer_in': case 'bonus_issue': return 'pos';
    case 'redemption': case 'transfer_out': return 'neg';
    case 'adjustment': return 'accent';
    default: return 'neutral';
  }
}
