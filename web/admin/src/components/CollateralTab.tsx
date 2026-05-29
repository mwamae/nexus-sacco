// Phase 1.5a — Collateral tab shared between ApplicationDetail
// (pre-disbursement) and LoanDetail (post-disbursement).
//
// Three regions per the prompt §5:
//   A — Security Coverage header card (rendered above the tab body
//       by the parent; we don't render it here so the parent decides
//       whether to fetch the coverage at all).
//   B — Items list with status-appropriate action buttons.
//   C — Add-collateral action bar (visible only when applicationId
//       is set; loans don't accept new collateral via this tab).
//
// Each item-row click opens the slide-over detail panel.

import { useEffect, useMemo, useState } from 'react';
import {
  createApplicationCollateral,
  deleteCollateral,
  getCollateralDetail,
  listApplicationCollateral,
  listLoanCollateral,
  pledgeCollateral,
  rejectCollateral,
  releaseCollateral,
  valueCollateral,
  verifyCollateral,
  type CollateralDetail,
  type CollateralEvent,
  type CollateralValuation,
  type LoanCollateralItem,
  type LoanCollateralKind,
} from '../api/client';
import { useAuth } from '../auth/AuthContext';

type Props = {
  applicationId?: string;
  loanId?: string;
  currency: string;
  // When the underlying loan/application is read-only (closed, etc.)
  // we hide the action bar + per-row actions.
  readOnly?: boolean;
  // Optional callback when an action mutates the row set — used by the
  // parent to refresh the coverage card.
  onChanged?: () => void;
};

const KIND_LABEL: Record<LoanCollateralKind, string> = {
  title_deed: 'Title deed',
  vehicle_logbook: 'Vehicle logbook',
  equipment: 'Equipment',
  listed_shares: 'Listed shares',
  fixed_deposit_lien: 'Fixed deposit (lien)',
  other: 'Other',
};

const STATUS_BG: Record<string, { bg: string; fg: string }> = {
  offered:   { bg: 'var(--surface-2, #f7f7f9)', fg: 'var(--fg, #333)' },
  verified:  { bg: 'var(--info-bg, #eaf3fb)',  fg: 'var(--info-fg, #0b5394)' },
  valued:    { bg: 'var(--warning-bg, #fff4d6)', fg: 'var(--warning-fg, #8a6d00)' },
  pledged:   { bg: 'var(--success-bg, #e7f4ec)', fg: 'var(--success-fg, #146c43)' },
  released:  { bg: 'var(--surface-2, #f7f7f9)', fg: 'var(--muted, #888)' },
  auctioned: { bg: 'var(--danger-bg, #fdecea)', fg: 'var(--danger-fg, #b42318)' },
};

function StatusPill({ s }: { s: string }) {
  const c = STATUS_BG[s] ?? { bg: '#eee', fg: '#333' };
  return (
    <span style={{
      background: c.bg, color: c.fg, padding: '3px 8px',
      borderRadius: 12, fontSize: 11, fontWeight: 600,
      textTransform: 'uppercase',
    }}>{s}</span>
  );
}

function fmtKES(s?: string): string {
  if (!s) return '—';
  const n = parseFloat(s);
  if (Number.isNaN(n)) return s;
  return n.toLocaleString('en-KE', { maximumFractionDigits: 2 });
}

export default function CollateralTab({ applicationId, loanId, currency, readOnly, onChanged }: Props) {
  const [items, setItems] = useState<LoanCollateralItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  const [openItem, setOpenItem] = useState<LoanCollateralItem | null>(null);
  const { hasPermission } = useAuth();

  async function refresh() {
    setLoading(true);
    setErr(null);
    try {
      const r = applicationId
        ? await listApplicationCollateral(applicationId)
        : loanId
          ? await listLoanCollateral(loanId)
          : { items: [], total: 0 };
      setItems(r.items);
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Failed to load collateral.');
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => { void refresh(); }, [applicationId, loanId]);

  function bumpParent() {
    onChanged?.();
    void refresh();
  }

  return (
    <div>
      {err && <div className="alert alert-error" style={{ marginBottom: 8 }}>{err}</div>}
      {loading ? (
        <div className="muted">Loading…</div>
      ) : items.length === 0 ? (
        <div className="empty" style={{ padding: 16 }}>
          No collateral on file.
        </div>
      ) : (
        <table className="tbl">
          <thead>
            <tr>
              <th>Kind</th>
              <th>Description</th>
              <th>Status</th>
              <th className="num">Est. value</th>
              <th className="num">FSV</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((c) => (
              <tr
                key={c.id}
                style={{ cursor: 'pointer' }}
                onClick={() => setOpenItem(c)}
              >
                <td>{KIND_LABEL[c.kind] ?? c.kind}</td>
                <td>{c.description}</td>
                <td><StatusPill s={c.status} /></td>
                <td className="num mono">{currency} {fmtKES(c.estimated_value)}</td>
                <td className="num mono">{c.forced_sale_value ? `${currency} ${fmtKES(c.forced_sale_value)}` : '—'}</td>
                <td onClick={(e) => e.stopPropagation()}>
                  <RowActions item={c} readOnly={readOnly} hasPerm={hasPermission} onChanged={bumpParent} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {/* Region C — Add bar */}
      {!readOnly && applicationId && (
        <div style={{ marginTop: 12, display: 'flex', justifyContent: 'flex-end' }}>
          <button
            className="btn btn-primary btn-sm"
            disabled={!hasPermission('loans:apply')}
            onClick={() => setAddOpen(true)}
          >
            + Add collateral
          </button>
        </div>
      )}

      {addOpen && applicationId && (
        <AddCollateralModal
          applicationId={applicationId}
          onClose={() => setAddOpen(false)}
          onSaved={() => { setAddOpen(false); bumpParent(); }}
        />
      )}

      {openItem && (
        <CollateralDetailDrawer
          id={openItem.id}
          currency={currency}
          readOnly={readOnly}
          onClose={() => setOpenItem(null)}
          onChanged={() => { bumpParent(); }}
        />
      )}
    </div>
  );
}

// ─────────── Row-level action buttons ───────────

function RowActions({
  item, readOnly, hasPerm, onChanged,
}: {
  item: LoanCollateralItem;
  readOnly?: boolean;
  hasPerm: (p: string) => boolean;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function run<T>(fn: () => Promise<T>) {
    setBusy(true); setErr(null);
    try { await fn(); onChanged(); } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Action failed.');
    } finally { setBusy(false); }
  }

  if (readOnly) return <span className="muted tiny">—</span>;

  return (
    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
      {item.status === 'offered' && hasPerm('loans:verify_collateral') && (
        <button className="btn btn-sm" disabled={busy} onClick={() => void run(async () => {
          const notes = window.prompt('Verification notes (what did you inspect?)');
          if (!notes?.trim()) return;
          await verifyCollateral(item.id, notes);
        })}>Verify</button>
      )}
      {item.status === 'offered' && hasPerm('loans:verify_collateral') && (
        <button className="btn btn-sm btn-link" disabled={busy} onClick={() => void run(async () => {
          const reason = window.prompt('Reject reason');
          if (!reason?.trim()) return;
          await rejectCollateral(item.id, reason);
        })}>Reject</button>
      )}
      {item.status === 'verified' && hasPerm('loans:value_collateral') && (
        <button className="btn btn-sm btn-primary" disabled={busy} onClick={() => void run(async () => {
          // For inline-quick use; the drawer's full valuation form covers detail.
          const market = window.prompt('Market value (KES)');
          const fsv = window.prompt('Forced-sale value (KES)');
          const valuer = window.prompt('Valuer name');
          if (!market || !fsv || !valuer) return;
          await valueCollateral(item.id, {
            valuer_name: valuer,
            valuation_date: new Date().toISOString().slice(0, 10),
            market_value: market,
            forced_sale_value: fsv,
          });
        })}>Add valuation</button>
      )}
      {item.status === 'valued' && hasPerm('loans:approve') && (
        <button className="btn btn-sm btn-primary" disabled={busy} onClick={() => void run(async () => {
          await pledgeCollateral(item.id);
        })}>Pledge</button>
      )}
      {item.status === 'pledged' && hasPerm('loans:approve') && (
        <button className="btn btn-sm btn-link" disabled={busy} onClick={() => void run(async () => {
          const reason = window.prompt('Release reason');
          if (!reason?.trim()) return;
          await releaseCollateral(item.id, reason);
        })}>Release</button>
      )}
      {(item.status === 'offered' || item.status === 'verified') && hasPerm('loans:apply') && (
        <button className="btn btn-sm btn-link" disabled={busy} onClick={() => void run(async () => {
          if (!window.confirm('Delete this collateral item?')) return;
          await deleteCollateral(item.id);
        })}>Delete</button>
      )}
      {err && <span className="alert-error" style={{ fontSize: 11 }}>{err}</span>}
    </div>
  );
}

// ─────────── Add-collateral modal ───────────

function AddCollateralModal({ applicationId, onClose, onSaved }: {
  applicationId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [kind, setKind] = useState<LoanCollateralKind>('title_deed');
  const [description, setDescription] = useState('');
  const [estimated, setEstimated] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    if (!description.trim() || !estimated.trim()) {
      setErr('Description + estimated value are required.');
      return;
    }
    setBusy(true); setErr(null);
    try {
      await createApplicationCollateral(applicationId, {
        kind,
        description: description.trim(),
        estimated_value: estimated.trim(),
        notes: notes.trim() || undefined,
      });
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.');
    } finally { setBusy(false); }
  }

  return (
    <Backdrop onClose={onClose}>
      <div style={{
        background: 'var(--surface)', padding: 20, borderRadius: 8,
        width: 'min(90vw, 480px)', maxHeight: '90vh', overflowY: 'auto',
      }}>
        <h3 style={{ marginTop: 0 }}>Add collateral</h3>
        {err && <div className="alert alert-error" style={{ marginBottom: 8 }}>{err}</div>}
        <label className="form-label">Kind</label>
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value as LoanCollateralKind)}>
          {(Object.keys(KIND_LABEL) as LoanCollateralKind[]).map((k) => (
            <option key={k} value={k}>{KIND_LABEL[k]}</option>
          ))}
        </select>
        <label className="form-label" style={{ marginTop: 10 }}>Description</label>
        <input className="input" placeholder="Plot LR12345, Nakuru" value={description} onChange={(e) => setDescription(e.target.value)} />
        <label className="form-label" style={{ marginTop: 10 }}>Estimated value (KES)</label>
        <input className="input" inputMode="decimal" placeholder="500000" value={estimated} onChange={(e) => setEstimated(e.target.value)} />
        <label className="form-label" style={{ marginTop: 10 }}>Notes (optional)</label>
        <textarea className="input" rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} />
        <div style={{ display: 'flex', gap: 6, justifyContent: 'flex-end', marginTop: 14 }}>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </Backdrop>
  );
}

function Backdrop({ children, onClose }: { children: React.ReactNode; onClose: () => void }) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.5)',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        zIndex: 1000,
      }}
    >
      <div onClick={(e) => e.stopPropagation()}>{children}</div>
    </div>
  );
}

// ─────────── Slide-over detail drawer ───────────

function CollateralDetailDrawer({
  id, currency, readOnly, onClose, onChanged,
}: {
  id: string;
  currency: string;
  readOnly?: boolean;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [d, setD] = useState<CollateralDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [tab, setTab] = useState<'timeline' | 'valuation' | 'docs'>('timeline');

  async function load() {
    setErr(null);
    try { setD(await getCollateralDetail(id)); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.'); }
  }
  useEffect(() => { void load(); }, [id]);

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed', inset: 0,
        background: 'rgba(0,0,0,0.5)', display: 'flex', justifyContent: 'flex-end',
        zIndex: 1000,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          width: 'min(600px, 95vw)', height: '100vh', background: 'var(--surface)',
          overflowY: 'auto', padding: 20, boxSizing: 'border-box',
        }}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>Collateral detail</h3>
          <button className="btn btn-sm" onClick={onClose}>Close</button>
        </div>
        {err && <div className="alert alert-error">{err}</div>}
        {!d ? <div className="muted">Loading…</div> : (
          <>
            <div style={{ marginBottom: 12 }}>
              <div className="muted tiny">{KIND_LABEL[d.item.kind] ?? d.item.kind}</div>
              <div style={{ fontWeight: 600, marginTop: 2 }}>{d.item.description}</div>
              <div style={{ marginTop: 6, display: 'flex', gap: 8, alignItems: 'center' }}>
                <StatusPill s={d.item.status} />
                <span className="muted tiny">
                  Est. {currency} {fmtKES(d.item.estimated_value)}
                  {d.item.forced_sale_value && <>  ·  FSV {currency} {fmtKES(d.item.forced_sale_value)}</>}
                </span>
              </div>
              {d.item.rejected_reason && (
                <div className="alert alert-warn" style={{ marginTop: 8 }}>
                  <strong>Rejected:</strong> {d.item.rejected_reason}
                </div>
              )}
              {d.item.released_reason && (
                <div className="muted tiny" style={{ marginTop: 8 }}>
                  Released: {d.item.released_reason}
                </div>
              )}
            </div>

            <div role="tablist" style={{ display: 'flex', gap: 6, borderBottom: '1px solid var(--border)', marginBottom: 12 }}>
              {(['timeline', 'valuation', 'docs'] as const).map((t) => (
                <button
                  key={t}
                  className={tab === t ? 'btn btn-sm btn-primary' : 'btn btn-sm'}
                  onClick={() => setTab(t)}
                >{t}</button>
              ))}
            </div>

            {tab === 'timeline' && <Timeline events={d.events} />}
            {tab === 'valuation' && <ValuationHistory history={d.valuation_history} currency={currency} />}
            {tab === 'docs' && <DocsPlaceholder item={d.item} />}

            {!readOnly && d.item.status !== 'released' && d.item.status !== 'auctioned' && (
              <div style={{ marginTop: 18, borderTop: '1px solid var(--border)', paddingTop: 12 }}>
                <div className="muted tiny" style={{ marginBottom: 6 }}>Actions appropriate for status:</div>
                <RowActions item={d.item} hasPerm={(p) => true /* drawer always shows; backend gates */}
                  onChanged={() => { void load(); onChanged(); }} />
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function Timeline({ events }: { events: CollateralEvent[] }) {
  if (events.length === 0) return <div className="muted">No events recorded yet.</div>;
  return (
    <ol style={{ paddingLeft: 18, margin: 0 }}>
      {events.map((e) => (
        <li key={e.id} style={{ marginBottom: 8 }}>
          <div style={{ fontWeight: 600, textTransform: 'capitalize' }}>{e.kind.replace(/_/g, ' ')}</div>
          <div className="muted tiny">{new Date(e.occurred_at).toLocaleString()}</div>
          {e.details && Object.keys(e.details).length > 0 && (
            <pre style={{ fontSize: 11, background: 'var(--surface-2, #f7f7f9)', padding: 6, borderRadius: 4, marginTop: 4 }}>
              {JSON.stringify(e.details, null, 2)}
            </pre>
          )}
        </li>
      ))}
    </ol>
  );
}

function ValuationHistory({ history, currency }: { history: CollateralValuation[]; currency: string }) {
  if (history.length === 0) return <div className="muted">No valuations on file. Use the Add Valuation action.</div>;
  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Date</th>
          <th>Valuer</th>
          <th className="num">Market</th>
          <th className="num">FSV</th>
          <th>Current</th>
        </tr>
      </thead>
      <tbody>
        {history.map((v) => (
          <tr key={v.id}>
            <td>{v.valuation_date}</td>
            <td>{v.valuer_name}</td>
            <td className="num mono">{currency} {fmtKES(v.market_value)}</td>
            <td className="num mono">{currency} {fmtKES(v.forced_sale_value)}</td>
            <td>{v.is_current ? '✓' : ''}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DocsPlaceholder({ item }: { item: LoanCollateralItem }) {
  const paths = [
    item.ownership_path && { label: 'Ownership document', path: item.ownership_path },
    ...(item.verification_photos ?? []).map((p, i) => ({ label: `Inspection photo ${i + 1}`, path: p })),
    item.current_valuation?.valuation_report_path && { label: 'Valuation report', path: item.current_valuation.valuation_report_path },
  ].filter(Boolean) as Array<{ label: string; path: string }>;
  if (paths.length === 0) return <div className="muted">No documents uploaded yet.</div>;
  return (
    <ul>
      {paths.map((p, i) => <li key={i}>{p.label}: <code>{p.path}</code></li>)}
    </ul>
  );
}
