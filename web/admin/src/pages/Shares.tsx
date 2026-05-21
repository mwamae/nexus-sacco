// Shares overview — tenant-wide share-capital position, register,
// bonus-issue runner, and policy editor.

import { useEffect, useState } from 'react';
import {
  bonusShareIssue,
  extractError,
  getSharePolicy,
  getShareSummary,
  listShareAccounts,
  updateSharePolicy,
  type ShareAccountListItem,
  type SharePolicy,
  type ShareSummary,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';
import { Avatar } from '../components/Avatar';
import { Badge, StatusBadge } from '../components/Badge';
import { Icon } from '../components/Icon';

export default function Shares() {
  const { hasPermission, tenant } = useAuth();
  const currency = tenant?.currency_code ?? 'KES';

  const canEditPolicy = hasPermission('tenant:settings:edit');
  const canBonusIssue = hasPermission('shares:bonus_issue');

  const [summary, setSummary] = useState<ShareSummary | null>(null);
  const [policy, setPolicy] = useState<SharePolicy | null>(null);
  const [items, setItems] = useState<ShareAccountListItem[]>([]);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState('');
  const [belowMin, setBelowMin] = useState(false);

  const [editPolicy, setEditPolicy] = useState(false);
  const [bonusOpen, setBonusOpen] = useState(false);

  async function reload() {
    setErr(null);
    try {
      const [s, p, l] = await Promise.all([
        getShareSummary(),
        getSharePolicy(),
        listShareAccounts({ q: q || undefined, below_min: belowMin, limit: 100 }),
      ]);
      setSummary(s); setPolicy(p);
      setItems(l.items ?? []); setTotal(l.total ?? 0);
    } catch (e) { setErr(extractError(e)); }
  }

  useEffect(() => { void reload(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [belowMin]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Capital · Equity</div>
          <h1>Shares</h1>
          <div className="page-sub">Member equity contributions — fixed par value, not deposits.</div>
        </div>
        <div className="page-hd-actions">
          {canBonusIssue && (
            <button className="btn btn-sm" onClick={() => setBonusOpen(true)}>
              <Icon name="plus" size={12} /> Bonus issue
            </button>
          )}
          {canEditPolicy && (
            <button className="btn btn-sm" onClick={() => setEditPolicy(true)}>Edit policy</button>
          )}
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {summary && policy && (
        <div className="grid-4" style={{ marginBottom: 14 }}>
          <KPI label="Total share capital" value={`${currency} ${fmtMoney(summary.total_share_capital)}`} sub={`${summary.total_shares_issued.toLocaleString()} shares × ${currency} ${policy.par_value}`} />
          <KPI label="Active accounts" value={String(summary.active_accounts)} sub={`${summary.total_accounts} total`} />
          <KPI label="Below minimum" value={String(summary.members_below_minimum)} sub={`min ${summary.min_shares_required} share${summary.min_shares_required === 1 ? '' : 's'}`} tone={summary.members_below_minimum > 0 ? 'warn' : undefined} />
          <KPI label="Pledged shares" value={String(summary.total_pledged_shares)} sub={`${summary.accounts_with_lien} account${summary.accounts_with_lien === 1 ? '' : 's'} with liens`} tone={summary.accounts_with_lien > 0 ? 'warn' : undefined} />
        </div>
      )}

      <div className="card">
        <div className="card-hd">
          <h3>Share register</h3>
          <span className="card-sub">{total.toLocaleString()} accounts</span>
          <div className="card-hd-actions">
            <form onSubmit={(e) => { e.preventDefault(); void reload(); }} style={{ display: 'flex', gap: 4 }}>
              <input
                className="input"
                style={{ height: 26, fontSize: 12, width: 220 }}
                placeholder="Search name / member # / account #"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
              <button className="btn btn-sm" type="submit"><Icon name="search" size={12} /></button>
            </form>
            <label style={{ display: 'flex', gap: 4, alignItems: 'center', fontSize: 12 }}>
              <input type="checkbox" checked={belowMin} onChange={(e) => setBelowMin(e.target.checked)} />
              <span>below minimum only</span>
            </label>
          </div>
        </div>
        <div className="card-body flush">
          {items.length === 0 ? (
            <div className="empty">{belowMin ? 'All active members meet the minimum.' : 'No share accounts yet.'}</div>
          ) : (
            <table className="tbl">
              <thead>
                <tr>
                  <th style={{ width: 44 }}></th>
                  <th>Member</th>
                  <th>Account</th>
                  <th>Status</th>
                  <th style={{ textAlign: 'right' }}>Shares</th>
                  <th style={{ textAlign: 'right' }}>Pledged</th>
                  <th style={{ textAlign: 'right' }}>Capital</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {items.map((it) => (
                  <tr key={it.account.id}>
                    <td><Avatar name={it.full_name} size="sm" /></td>
                    <td>
                      <div style={{ fontWeight: 500 }}>
                        <a href={`/members/${it.account.member_id}?tab=accounts`} className="tbl-link">{it.full_name}</a>
                      </div>
                      <div className="tiny-mono">{it.member_no}</div>
                    </td>
                    <td className="tiny-mono">{it.account.account_no}</td>
                    <td>
                      <StatusBadge status={it.member_status} />
                      {it.account.status === 'closed' && <span> · <Badge tone="neutral">closed</Badge></span>}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{it.account.shares_held}</td>
                    <td className="mono" style={{ textAlign: 'right', color: it.account.shares_pledged > 0 ? 'var(--warn)' : undefined }}>
                      {it.account.shares_pledged || '—'}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{currency} {fmtMoney(it.account.total_value)}</td>
                    <td>
                      <a className="btn btn-sm" href={`/members/${it.account.member_id}?tab=accounts`} title="Open">
                        <Icon name="eye" size={12} />
                      </a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {editPolicy && policy && (
        <PolicyModal
          initial={policy}
          currency={currency}
          onClose={() => setEditPolicy(false)}
          onSave={async (p) => {
            try { await updateSharePolicy(p); setEditPolicy(false); await reload(); }
            catch (e) { alert(extractError(e)); }
          }}
        />
      )}

      {bonusOpen && (
        <BonusIssueModal
          onClose={() => setBonusOpen(false)}
          onRun={async (pct, reason) => {
            try {
              const r = await bonusShareIssue({ pct_of_holding: pct, reason });
              if (r.pending) {
                alert(`Queued for approval. Pending id: ${r.pending.id.slice(0, 8)}…`);
              } else if (r.posted) {
                alert(`Issued ${r.posted.total_bonus_shares} bonus shares to ${r.posted.issued_to_count} member${r.posted.issued_to_count === 1 ? '' : 's'} (${r.posted.pct_applied}% of holding).`);
              }
              setBonusOpen(false);
              await reload();
            } catch (e) { alert(extractError(e)); }
          }}
        />
      )}
    </div>
  );
}

// ─────────── KPI ───────────

function KPI({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: 'pos' | 'neg' | 'warn' }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'neg' ? 'var(--neg)' : tone === 'warn' ? 'var(--warn)' : 'var(--fg)';
  return (
    <div className="card">
      <div className="kpi">
        <div className="kpi-label">{label}</div>
        <div className="kpi-value mono" style={{ color }}>{value}</div>
        {sub && <div className="muted tiny">{sub}</div>}
      </div>
    </div>
  );
}

// ─────────── Policy modal ───────────

function PolicyModal({ initial, currency, onClose, onSave }: {
  initial: SharePolicy;
  currency: string;
  onClose: () => void;
  onSave: (p: SharePolicy) => void;
}) {
  const [par, setPar] = useState(initial.par_value);
  const [min, setMin] = useState(initial.min_shares_required);
  const [maxPct, setMaxPct] = useState(initial.max_shares_pct_of_capital);
  const [prefix, setPrefix] = useState(initial.certificate_prefix);
  return (
    <ModalShell title="Share policy" onClose={onClose}
      onSubmit={() => onSave({ par_value: par, min_shares_required: min, max_shares_pct_of_capital: maxPct, certificate_prefix: prefix })}
      submitLabel="Save policy" disabled={!par || parseFloat(par) <= 0}>
      <Field label={`Par value (${currency} per share)`}>
        <input className="input mono" value={par} onChange={(e) => setPar(e.target.value)} />
      </Field>
      <Field label="Minimum shares required">
        <input className="input mono" type="number" min={0} value={min} onChange={(e) => setMin(parseInt(e.target.value, 10) || 0)} />
      </Field>
      <Field label="Maximum share holding (% of total share capital — 0 = uncapped)">
        <input className="input mono" value={maxPct} onChange={(e) => setMaxPct(e.target.value)} />
      </Field>
      <Field label="Certificate number prefix">
        <input className="input mono" value={prefix} onChange={(e) => setPrefix(e.target.value)} />
      </Field>
    </ModalShell>
  );
}

// ─────────── Bonus issue modal ───────────

function BonusIssueModal({ onClose, onRun }: {
  onClose: () => void;
  onRun: (pct: string, reason: string) => void;
}) {
  const [pct, setPct] = useState('5');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  return (
    <ModalShell title="Bonus share issue" onClose={onClose}
      onSubmit={async () => {
        if (!confirm(`Issue ${pct}% bonus shares to every active member with holdings? This cannot be undone.`)) return;
        setBusy(true); await onRun(pct, reason); setBusy(false);
      }}
      submitLabel={busy ? 'Running…' : 'Run bonus issue'}
      disabled={busy || parseFloat(pct) <= 0 || reason.trim() === ''}>
      <div className="alert alert-warn">
        Bonus share issues require an <strong>AGM resolution</strong>. Capture the resolution reference in the reason field below — it goes into the audit trail of every member transaction.
      </div>
      <Field label="Percentage of current holding">
        <input className="input mono" value={pct} onChange={(e) => setPct(e.target.value)} placeholder="5" />
        <div className="muted tiny" style={{ marginTop: 4 }}>e.g. "5" means every member receives 5% bonus shares (rounded down).</div>
      </Field>
      <Field label="AGM resolution reference (audit)">
        <textarea className="input" rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. AGM resolution 2026-03, 5% bonus shares issue capitalising statutory reserve" />
      </Field>
    </ModalShell>
  );
}

// ─────────── Shared modal shell ───────────

function ModalShell({ title, onClose, onSubmit, children, submitLabel, disabled }: {
  title: string; onClose: () => void; onSubmit: () => void | Promise<void>;
  children: React.ReactNode; submitLabel: string; disabled?: boolean;
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
      <div className="card" style={{ width: 540, maxWidth: '90vw' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd">
          <h3>{title}</h3>
          <div className="card-hd-actions">
            <button className="btn btn-sm btn-ghost" onClick={onClose}><Icon name="x" size={12} /></button>
          </div>
        </div>
        <div className="card-body">{children}</div>
        <div className="card-body" style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', borderTop: '1px solid var(--border)' }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={disabled} onClick={() => void onSubmit()}>{submitLabel}</button>
        </div>
      </div>
    </div>
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
