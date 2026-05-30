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
  markCollateralAuctioned,
  valueCollateral,
  uploadValuationReport,
  valuationReportDownloadURL,
  collateralOwnershipDocURL,
  collateralVerificationPhotoURL,
  openAuthedFile,
  verifyCollateral,
  // Phase 1.5b
  recordCollateralCharge,
  dischargeCollateralCharge,
  recordCollateralInsurance,
  getCollateralInsuranceHistory,
  recordCollateralCustody,
  getCollateralCustodyTimeline,
  recordCollateralAuctionEvent,
  getCollateralAuctionEvents,
  issuePledgerConsent,
  type ChargeRegistry,
  type CollateralDetail,
  type CollateralEvent,
  type CollateralInsurancePolicy,
  type CollateralValuation,
  type CustodyMovement,
  type CustodyMovementRow,
  type AuctionEventKind,
  type AuctionEventRow,
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
  const [valuationOpen, setValuationOpen] = useState(false);
  const [auctionOpen, setAuctionOpen] = useState(false);

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
        <button className="btn btn-sm btn-primary" disabled={busy} onClick={() => setValuationOpen(true)}>
          Add valuation
        </button>
      )}
      {valuationOpen && (
        <ValuationModal
          collateralId={item.id}
          onClose={() => setValuationOpen(false)}
          onSaved={() => { setValuationOpen(false); onChanged(); }}
        />
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
      {item.status === 'pledged' && hasPerm('loans:auction') && (
        <>
          <button
            className="btn btn-sm"
            disabled={busy}
            style={{ color: 'var(--danger-fg, #b42318)' }}
            onClick={() => setAuctionOpen(true)}
          >Mark auctioned</button>
          {auctionOpen && (
            <MarkAuctionedModal
              item={item}
              onClose={() => setAuctionOpen(false)}
              onSaved={() => { setAuctionOpen(false); onChanged(); }}
            />
          )}
        </>
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

// ─────────── Add-collateral modal ───────────

// Kind catalogue: icon + short hint + descriptive placeholder. Keeps the
// kind picker readable without cramming a long enum dropdown.
const KIND_META: Record<LoanCollateralKind, { icon: string; hint: string; descPlaceholder: string }> = {
  title_deed:         { icon: '🏠', hint: 'Land / property title', descPlaceholder: 'e.g. Plot LR 12345, Nakuru — Sub-division 4' },
  vehicle_logbook:    { icon: '🚗', hint: 'Motor-vehicle logbook',  descPlaceholder: 'e.g. 2019 Toyota Hilux · KCD 123A' },
  equipment:          { icon: '⚙️',  hint: 'Industrial / business equipment', descPlaceholder: 'e.g. CNC milling machine, model X1' },
  listed_shares:      { icon: '📈', hint: 'NSE-listed shares',      descPlaceholder: 'e.g. 1,000 SCOM shares held via Dyer & Blair' },
  fixed_deposit_lien: { icon: '💰', hint: 'Lien on a deposit',      descPlaceholder: 'e.g. Fixed deposit a/c 100-203, 12-month tenor' },
  other:              { icon: '📦', hint: 'Anything else',           descPlaceholder: 'Describe the asset in detail.' },
};

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

  const meta = KIND_META[kind];
  const canSave = description.trim().length > 0 && estimated.trim().length > 0;

  async function save() {
    if (!canSave) {
      setErr('Description and estimated value are required.');
      return;
    }
    const est = parseFloat(estimated.trim());
    if (Number.isNaN(est) || est <= 0) {
      setErr('Estimated value must be a positive number.');
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
    <ModalShell
      title="Add collateral"
      subtitle="Capture a new asset to back this loan application. Internal kinds (fixed-deposit lien, listed shares) are placed immediately; external kinds walk through verify → value → pledge."
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !canSave} onClick={() => void save()}>
            {busy ? 'Saving…' : 'Save collateral'}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>}

      <FormSection label="Kind of asset" hint="Pick what's being pledged. The kind drives which extra checks the system runs.">
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          {(Object.keys(KIND_META) as LoanCollateralKind[]).map((k) => {
            const selected = kind === k;
            const m = KIND_META[k];
            return (
              <button
                key={k}
                type="button"
                onClick={() => setKind(k)}
                style={{
                  textAlign: 'left',
                  display: 'flex', gap: 10, alignItems: 'flex-start',
                  padding: '10px 12px',
                  border: `2px solid ${selected ? 'var(--accent, #2563eb)' : 'var(--border, #eee)'}`,
                  background: selected ? 'var(--surface-2, #fafafa)' : 'var(--surface, white)',
                  borderRadius: 8, cursor: 'pointer',
                }}
              >
                <span style={{ fontSize: 20, lineHeight: 1 }}>{m.icon}</span>
                <span>
                  <div style={{ fontWeight: 600, fontSize: 13 }}>{KIND_LABEL[k]}</div>
                  <div className="muted tiny" style={{ marginTop: 2 }}>{m.hint}</div>
                </span>
              </button>
            );
          })}
        </div>
      </FormSection>

      <FormSection label="Description" hint="Specific identifier — the more concrete, the easier the inspection.">
        <input
          className="input"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={meta.descPlaceholder}
          autoFocus
        />
      </FormSection>

      <FormSection
        label="Estimated value (KES)"
        hint="Officer's estimate. A panel valuation can attach later and replace this for coverage purposes."
      >
        <div style={{ display: 'flex', alignItems: 'stretch' }}>
          <div style={{
            display: 'flex', alignItems: 'center',
            padding: '0 12px',
            background: 'var(--surface-2, #f5f5f5)',
            border: '1px solid var(--border, #ddd)',
            borderRight: 'none',
            borderRadius: '6px 0 0 6px',
            fontWeight: 600, color: 'var(--muted, #6b7280)',
          }}>KES</div>
          <input
            className="input"
            style={{ borderRadius: '0 6px 6px 0', flex: 1 }}
            inputMode="decimal"
            value={estimated}
            onChange={(e) => setEstimated(e.target.value.replace(/[^0-9.]/g, ''))}
            placeholder="500,000"
          />
        </div>
      </FormSection>

      <FormSection label="Notes" hint="Optional. Any context for the verifier (access details, condition, etc).">
        <textarea
          className="input"
          rows={2}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder="Optional…"
        />
      </FormSection>

      <div className="muted tiny" style={{
        padding: 10,
        background: 'var(--info-bg, #eaf3fb)',
        color: 'var(--info-fg, #0b5394)',
        borderRadius: 6,
      }}>
        After saving, this item enters as <strong>offered</strong>. It must be{' '}
        <strong>verified</strong>, <strong>valued</strong>, and <strong>pledged</strong> before
        it contributes to security coverage.
      </div>
    </ModalShell>
  );
}

// ─────────── Modal scaffolding (local) ───────────

function ModalShell({ title, subtitle, onClose, children, footer, maxWidth = 560 }: {
  title: string;
  subtitle?: string;
  onClose: () => void;
  children: React.ReactNode;
  footer: React.ReactNode;
  maxWidth?: number;
}) {
  return (
    <Backdrop onClose={onClose}>
      <div style={{
        background: 'var(--surface)',
        borderRadius: 10,
        width: `min(94vw, ${maxWidth}px)`,
        boxShadow: '0 20px 60px rgba(0,0,0,0.25)',
        display: 'flex', flexDirection: 'column',
        maxHeight: '90vh',
      }}>
        <div style={{
          padding: '16px 20px',
          borderBottom: '1px solid var(--border, #eee)',
          display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 12,
        }}>
          <div>
            <h3 style={{ margin: 0, fontSize: 18 }}>{title}</h3>
            {subtitle && <div className="muted tiny" style={{ marginTop: 4, lineHeight: 1.4 }}>{subtitle}</div>}
          </div>
          <button
            className="btn btn-sm btn-link"
            onClick={onClose}
            aria-label="Close"
            style={{ fontSize: 20, lineHeight: 1, padding: '0 8px' }}
          >×</button>
        </div>
        <div style={{ padding: '16px 20px', overflowY: 'auto', flex: 1 }}>
          {children}
        </div>
        <div style={{
          padding: '12px 20px',
          borderTop: '1px solid var(--border, #eee)',
          display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap',
          background: 'var(--surface-2, #fafafa)',
          borderRadius: '0 0 10px 10px',
        }}>
          {footer}
        </div>
      </div>
    </Backdrop>
  );
}

function FormSection({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <label className="form-label" style={{
        display: 'block', marginBottom: 4,
        fontSize: 13, fontWeight: 500,
      }}>{label}</label>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4, lineHeight: 1.4 }}>{hint}</div>}
    </div>
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
  type DrawerTab = 'timeline' | 'valuation' | 'docs' | 'charge' | 'insurance' | 'custody' | 'auction';
  const [tab, setTab] = useState<DrawerTab>('timeline');

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

            <div role="tablist" style={{ display: 'flex', gap: 6, borderBottom: '1px solid var(--border)', marginBottom: 12, flexWrap: 'wrap' }}>
              {(['timeline', 'valuation', 'docs', 'charge', 'insurance', 'custody', 'auction'] as const).map((t) => (
                <button
                  key={t}
                  className={tab === t ? 'btn btn-sm btn-primary' : 'btn btn-sm'}
                  onClick={() => setTab(t)}
                >{t}</button>
              ))}
            </div>

            {tab === 'timeline' && <Timeline events={d.events} />}
            {tab === 'valuation' && (
              <ValuationHistory
                history={d.valuation_history}
                currency={currency}
                item={d.item}
                readOnly={readOnly}
                onAdded={() => { void load(); onChanged(); }}
              />
            )}
            {tab === 'docs' && <DocsSubTab item={d.item} />}
            {tab === 'charge' && <ChargeSubTab item={d.item} onChanged={() => { void load(); onChanged(); }} />}
            {tab === 'insurance' && <InsuranceSubTab collateralId={d.item.id} currency={currency} onChanged={() => { void load(); onChanged(); }} />}
            {tab === 'custody' && <CustodySubTab collateralId={d.item.id} onChanged={() => { void load(); onChanged(); }} />}
            {tab === 'auction' && <AuctionSubTab item={d.item} currency={currency} onChanged={() => { void load(); onChanged(); }} />}

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

function Timeline({ events }: { events: CollateralEvent[] | null | undefined }) {
  const rows = events ?? [];
  if (rows.length === 0) return <div className="muted">No events recorded yet.</div>;
  return (
    <ol style={{ paddingLeft: 18, margin: 0 }}>
      {rows.map((e) => (
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

function ValuationHistory({
  history, currency, item, readOnly, onAdded,
}: {
  history: CollateralValuation[] | null | undefined;
  currency: string;
  item: LoanCollateralItem;
  readOnly?: boolean;
  onAdded: () => void;
}) {
  const { hasPermission } = useAuth();
  const [open, setOpen] = useState(false);
  const rows = history ?? [];

  const canAdd = !readOnly
    && hasPermission('loans:value_collateral')
    && item.status !== 'released'
    && item.status !== 'auctioned';

  return (
    <div>
      {canAdd && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
          <button className="btn btn-sm btn-primary" onClick={() => setOpen(true)}>
            + Add valuation
          </button>
        </div>
      )}
      {rows.length === 0 ? (
        <div className="muted">
          No valuations on file.
          {canAdd
            ? ' Click "Add valuation" above to attach a panel-valuer report.'
            : ' A panel-valuer report attaches here once the asset is verified.'}
        </div>
      ) : (
        <table className="tbl">
          <thead>
            <tr>
              <th>Date</th>
              <th>Valuer</th>
              <th className="num">Market</th>
              <th className="num">FSV</th>
              <th>Expires</th>
              <th>Current</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((v) => (
              <tr key={v.id}>
                <td>
                  {v.valuation_date}
                  {v.valuation_report_path && (
                    <div style={{ marginTop: 2 }}>
                      <button
                        type="button"
                        className="btn btn-sm btn-link"
                        style={{ padding: 0, fontSize: 11 }}
                        onClick={() => { void openAuthedFile(valuationReportDownloadURL(v.id)); }}
                      >📄 View report ↗</button>
                    </div>
                  )}
                </td>
                <td>
                  {v.valuer_name}
                  {v.valuer_contact && <div className="muted tiny">{v.valuer_contact}</div>}
                </td>
                <td className="num mono">{currency} {fmtKES(v.market_value)}</td>
                <td className="num mono">{currency} {fmtKES(v.forced_sale_value)}</td>
                <td className="tiny">{v.expires_at ?? '—'}</td>
                <td>{v.is_current ? '✓' : ''}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {open && (
        <ValuationModal
          collateralId={item.id}
          onClose={() => setOpen(false)}
          onSaved={() => { setOpen(false); onAdded(); }}
        />
      )}
    </div>
  );
}

// ─────────── Add-valuation modal ───────────

function ValuationModal({ collateralId, onClose, onSaved }: {
  collateralId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const today = new Date().toISOString().slice(0, 10);
  const [valuerName, setValuerName] = useState('');
  const [valuerContact, setValuerContact] = useState('');
  const [valuationDate, setValuationDate] = useState(today);
  const [marketValue, setMarketValue] = useState('');
  const [fsv, setFSV] = useState('');
  const [reportFile, setReportFile] = useState<File | null>(null);
  const [expiresAt, setExpiresAt] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [busyStage, setBusyStage] = useState('');
  const [err, setErr] = useState<string | null>(null);

  const canSave = valuerName.trim() && marketValue.trim() && fsv.trim() && valuationDate;
  const mv = parseFloat(marketValue || '0');
  const fv = parseFloat(fsv || '0');
  const fsvOverMarket = fv > 0 && mv > 0 && fv > mv;
  const fsvHaircut = fv > 0 && mv > 0 ? ((1 - fv / mv) * 100).toFixed(1) : null;

  async function save() {
    if (!canSave) { setErr('Valuer name, date, market value, and FSV are required.'); return; }
    if (fsvOverMarket) { setErr('Forced-sale value cannot exceed market value.'); return; }
    if (mv <= 0 || fv <= 0) { setErr('Market value and FSV must be positive.'); return; }
    setBusy(true); setErr(null);
    try {
      let reportPath: string | undefined;
      if (reportFile) {
        setBusyStage('Uploading report…');
        const up = await uploadValuationReport(collateralId, reportFile);
        reportPath = up.storage_path;
      }
      setBusyStage('Saving valuation…');
      await valueCollateral(collateralId, {
        valuer_name: valuerName.trim(),
        valuer_contact: valuerContact.trim() || undefined,
        valuation_date: valuationDate,
        market_value: marketValue.trim(),
        forced_sale_value: fsv.trim(),
        valuation_report_path: reportPath,
        expires_at: expiresAt || undefined,
        notes: notes.trim() || undefined,
      });
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.');
    } finally { setBusy(false); setBusyStage(''); }
  }

  return (
    <ModalShell
      title="Add valuation"
      subtitle="Attach a panel-valuer report so the asset can move from verified → valued. Forced-sale value is what counts toward coverage."
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !canSave} onClick={() => void save()}>
            {busy ? (busyStage || 'Saving…') : 'Save valuation'}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>}

      <FormSection label="Valuer" hint="The accredited firm or individual that produced the report.">
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <input
            className="input"
            placeholder="Firm or valuer name"
            value={valuerName}
            onChange={(e) => setValuerName(e.target.value)}
            autoFocus
          />
          <input
            className="input"
            placeholder="Contact (phone / email — optional)"
            value={valuerContact}
            onChange={(e) => setValuerContact(e.target.value)}
          />
        </div>
      </FormSection>

      <FormSection label="Valuation date">
        <input
          className="input"
          type="date"
          value={valuationDate}
          onChange={(e) => setValuationDate(e.target.value)}
          max={today}
        />
      </FormSection>

      <FormSection
        label="Amounts"
        hint={
          fsvHaircut && !fsvOverMarket
            ? `FSV is ${fsvHaircut}% below market — typical haircut.`
            : fsvOverMarket
              ? 'FSV cannot exceed market value.'
              : 'Forced-sale value is what counts toward the coverage gate.'
        }
      >
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <KESInput value={marketValue} onChange={setMarketValue} placeholder="Market value" />
          <KESInput value={fsv} onChange={setFSV} placeholder="Forced-sale value" />
        </div>
      </FormSection>

      <FormSection
        label="Valuation report"
        hint={reportFile
          ? `${reportFile.name} · ${Math.round(reportFile.size / 1024).toLocaleString()} KB — will upload + link to this collateral.`
          : 'PDF or photo of the panel-valuer report. 10 MB max.'}
      >
        <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input
            className="input"
            type="file"
            accept=".pdf,image/png,image/jpeg"
            onChange={(e) => setReportFile(e.target.files?.[0] ?? null)}
            style={{ flex: 1 }}
          />
          {reportFile && (
            <button
              type="button"
              className="btn btn-sm btn-link"
              onClick={() => setReportFile(null)}
            >Clear</button>
          )}
        </div>
      </FormSection>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <FormSection label="Valid until" hint="Sets the revaluation-reminder window.">
          <input
            className="input"
            type="date"
            value={expiresAt}
            min={valuationDate}
            onChange={(e) => setExpiresAt(e.target.value)}
          />
        </FormSection>
        <FormSection label="Notes" hint="Optional — caveats, methodology, condition.">
          <input
            className="input"
            value={notes}
            onChange={(e) => setNotes(e.target.value)}
            placeholder="Optional…"
          />
        </FormSection>
      </div>

      <div className="muted tiny" style={{
        padding: 10,
        background: 'var(--info-bg, #eaf3fb)',
        color: 'var(--info-fg, #0b5394)',
        borderRadius: 6,
      }}>
        Saving will set this as the current valuation. Any prior current valuation is preserved in the
        history and marked superseded.
      </div>
    </ModalShell>
  );
}

function KESInput({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'stretch' }}>
      <div style={{
        display: 'flex', alignItems: 'center', padding: '0 10px',
        background: 'var(--surface-2, #f5f5f5)',
        border: '1px solid var(--border, #ddd)', borderRight: 'none',
        borderRadius: '6px 0 0 6px', fontWeight: 600, color: 'var(--muted, #6b7280)',
        fontSize: 12,
      }}>KES</div>
      <input
        className="input"
        style={{ borderRadius: '0 6px 6px 0', flex: 1 }}
        inputMode="decimal"
        value={value}
        onChange={(e) => onChange(e.target.value.replace(/[^0-9.]/g, ''))}
        placeholder={placeholder}
      />
    </div>
  );
}

// File entry shape for the Docs sub-tab — typed so the icon + open
// handler stay in one place.
type DocFile = {
  label: string;
  sublabel?: string;
  icon: string;
  onOpen: () => Promise<void>;
};

function DocsSubTab({ item }: { item: LoanCollateralItem }) {
  const files: DocFile[] = [];

  if (item.ownership_path) {
    files.push({
      label: 'Ownership document',
      sublabel: 'Title / logbook attached at intake',
      icon: '📄',
      onOpen: () => openAuthedFile(collateralOwnershipDocURL(item.id)),
    });
  }
  (item.verification_photos ?? []).forEach((_p, i) => {
    files.push({
      label: `Inspection photo ${i + 1}`,
      sublabel: 'Captured during verification',
      icon: '🖼️',
      onOpen: () => openAuthedFile(collateralVerificationPhotoURL(item.id, i)),
    });
  });
  if (item.current_valuation?.valuation_report_path && item.current_valuation?.id) {
    const v = item.current_valuation;
    files.push({
      label: 'Valuation report',
      sublabel: `${v.valuer_name}${v.valuation_date ? ` · ${v.valuation_date}` : ''}`,
      icon: '📑',
      onOpen: () => openAuthedFile(valuationReportDownloadURL(v.id)),
    });
  }

  if (files.length === 0) {
    return (
      <div className="muted" style={{ padding: 12 }}>
        No files attached yet. Upload happens during verify (inspection photos),
        intake (ownership doc), and valuation (report PDF).
      </div>
    );
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {files.map((f, i) => (
        <DocFileRow key={i} file={f} />
      ))}
    </div>
  );
}

function DocFileRow({ file }: { file: DocFile }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 12,
      padding: '10px 12px',
      border: '1px solid var(--border, #eee)',
      borderRadius: 8,
      background: 'var(--surface, white)',
    }}>
      <span style={{ fontSize: 22, lineHeight: 1 }}>{file.icon}</span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, fontSize: 13 }}>{file.label}</div>
        {file.sublabel && <div className="muted tiny" style={{ marginTop: 2 }}>{file.sublabel}</div>}
        {err && <div className="alert-error tiny" style={{ marginTop: 4, color: 'var(--danger-fg, #b42318)' }}>{err}</div>}
      </div>
      <button
        type="button"
        className="btn btn-sm"
        disabled={busy}
        onClick={async () => {
          setBusy(true); setErr(null);
          try { await file.onOpen(); }
          catch (e: any) {
            setErr(e?.response?.data?.error?.message || e?.message || 'Could not open.');
          } finally { setBusy(false); }
        }}
      >{busy ? 'Opening…' : 'Open ↗'}</button>
    </div>
  );
}

// ─────────── Phase 1.5b — Charge sub-tab ───────────

// ─────────── Charge sub-tab ───────────

const CHARGE_REGISTRY_META: Record<ChargeRegistry, { icon: string; label: string; hint: string }> = {
  lands_registry:        { icon: '🏛️',  label: 'Lands Registry',         hint: 'Title-deed charge / caveat' },
  ntsa:                  { icon: '🚗',  label: 'NTSA',                    hint: 'Vehicle logbook chattels mortgage' },
  stockbroker_custodian: { icon: '📈',  label: 'Stockbroker / Custodian', hint: 'Listed shares custody' },
  kra:                   { icon: '🧾',  label: 'KRA',                     hint: 'Customs / excise security' },
  other:                 { icon: '📦',  label: 'Other',                   hint: 'Anything else' },
};

function ChargeSubTab({ item, onChanged }: { item: LoanCollateralItem; onChanged: () => void }) {
  const { hasPermission } = useAuth();
  const canEdit = hasPermission('loans:charge_registration');
  const [editOpen, setEditOpen] = useState(false);
  const [dischargeOpen, setDischargeOpen] = useState(false);

  const registered = !!item.charge_registered_at;
  const discharged = !!item.charge_discharged_at;
  const regMeta = item.charge_registry ? CHARGE_REGISTRY_META[item.charge_registry as ChargeRegistry] : null;

  // Status pill colors.
  const status = discharged
    ? { bg: 'var(--surface-2, #f7f7f9)', fg: 'var(--muted, #6b7280)', label: '🔓 Discharged' }
    : registered
      ? { bg: 'var(--success-bg, #e7f4ec)', fg: 'var(--success-fg, #146c43)', label: '🔒 Registered' }
      : { bg: 'var(--warning-bg, #fff4d6)', fg: 'var(--warning-fg, #8a6d00)', label: '⚠ Not registered' };

  return (
    <div>
      {/* Status banner */}
      <div style={{
        padding: 14,
        background: status.bg, color: status.fg,
        borderRadius: 8,
        marginBottom: 12,
        display: 'flex', alignItems: 'flex-start', gap: 14,
      }}>
        <div style={{ fontSize: 24, lineHeight: 1 }}>{regMeta?.icon ?? '📜'}</div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            <span style={{
              padding: '2px 8px', borderRadius: 12,
              background: 'rgba(255,255,255,0.6)', fontSize: 11, fontWeight: 700,
            }}>{status.label}</span>
            {regMeta && <strong>{regMeta.label}</strong>}
          </div>
          {!registered && (
            <div className="tiny" style={{ marginTop: 6 }}>
              Legal charge has not been filed yet. Approval will block until this is recorded for kinds in tenant policy.
            </div>
          )}
          {registered && (
            <div style={{ marginTop: 6, display: 'grid', gridTemplateColumns: 'auto 1fr', columnGap: 14, rowGap: 4, fontSize: 13 }}>
              <span className="muted tiny">Reference</span>
              <span className="mono">{item.charge_reference || '—'}</span>
              <span className="muted tiny">Registered</span>
              <span>{item.charge_registered_at?.slice(0, 10)}</span>
              {item.charge_discharged_at && (
                <>
                  <span className="muted tiny">Discharged</span>
                  <span>{item.charge_discharged_at.slice(0, 10)}{item.charge_discharge_ref ? ` · ref ${item.charge_discharge_ref}` : ''}</span>
                </>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Actions */}
      {canEdit && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {!registered && (
            <button className="btn btn-primary btn-sm" onClick={() => setEditOpen(true)}>
              + Record charge
            </button>
          )}
          {registered && !discharged && (
            <>
              <button className="btn btn-sm" onClick={() => setEditOpen(true)}>Edit details</button>
              <button className="btn btn-sm" onClick={() => setDischargeOpen(true)}>🔓 Discharge</button>
            </>
          )}
          {discharged && (
            <span className="muted tiny" style={{ alignSelf: 'center' }}>
              Charge cycle complete — no further actions.
            </span>
          )}
        </div>
      )}
      {!canEdit && (
        <div className="muted tiny">
          You don&rsquo;t have <code>loans:charge_registration</code> — view only.
        </div>
      )}

      {editOpen && (
        <ChargeEditModal
          item={item}
          onClose={() => setEditOpen(false)}
          onSaved={() => { setEditOpen(false); onChanged(); }}
        />
      )}
      {dischargeOpen && (
        <ChargeDischargeModal
          item={item}
          onClose={() => setDischargeOpen(false)}
          onSaved={() => { setDischargeOpen(false); onChanged(); }}
        />
      )}
    </div>
  );
}

function ChargeEditModal({ item, onClose, onSaved }: {
  item: LoanCollateralItem;
  onClose: () => void;
  onSaved: () => void;
}) {
  const today = new Date().toISOString().slice(0, 10);
  const isEdit = !!item.charge_registered_at;
  const [registry, setRegistry] = useState<ChargeRegistry>(item.charge_registry as ChargeRegistry || 'lands_registry');
  const [reference, setReference] = useState(item.charge_reference || '');
  const [registeredAt, setRegisteredAt] = useState(item.charge_registered_at?.slice(0, 10) || today);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const canSave = reference.trim().length > 0;

  async function save() {
    if (!canSave) { setErr('Reference / filing number is required.'); return; }
    setBusy(true); setErr(null);
    try {
      await recordCollateralCharge(item.id, {
        registry,
        reference: reference.trim(),
        registered_at: registeredAt,
      });
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.');
    } finally { setBusy(false); }
  }

  return (
    <ModalShell
      title={isEdit ? 'Edit charge details' : 'Record legal charge'}
      subtitle="Capture the registry filing details so approval can clear and the audit trail is complete."
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !canSave} onClick={() => void save()}>
            {busy ? 'Saving…' : (isEdit ? 'Update' : 'Record charge')}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>}

      <FormSection label="Registry" hint="Which body holds the filing.">
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          {(Object.keys(CHARGE_REGISTRY_META) as ChargeRegistry[]).map((r) => {
            const meta = CHARGE_REGISTRY_META[r];
            const selected = registry === r;
            return (
              <button
                key={r}
                type="button"
                onClick={() => setRegistry(r)}
                style={{
                  textAlign: 'left',
                  display: 'flex', gap: 10, alignItems: 'flex-start',
                  padding: '10px 12px',
                  border: `2px solid ${selected ? 'var(--accent, #2563eb)' : 'var(--border, #eee)'}`,
                  background: selected ? 'var(--surface-2, #fafafa)' : 'var(--surface, white)',
                  borderRadius: 8, cursor: 'pointer',
                }}
              >
                <span style={{ fontSize: 20, lineHeight: 1 }}>{meta.icon}</span>
                <span>
                  <div style={{ fontWeight: 600, fontSize: 13 }}>{meta.label}</div>
                  <div className="muted tiny" style={{ marginTop: 2 }}>{meta.hint}</div>
                </span>
              </button>
            );
          })}
        </div>
      </FormSection>

      <FormSection label="Reference / filing number" hint="The registry's stamp / file number — the source of truth for audit.">
        <input
          className="input"
          value={reference}
          onChange={(e) => setReference(e.target.value)}
          placeholder="e.g. CL/2026/12345"
          autoFocus
        />
      </FormSection>

      <FormSection label="Registered date">
        <input
          className="input"
          type="date"
          value={registeredAt}
          max={today}
          onChange={(e) => setRegisteredAt(e.target.value)}
        />
      </FormSection>
    </ModalShell>
  );
}

function ChargeDischargeModal({ item, onClose, onSaved }: {
  item: LoanCollateralItem;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [reference, setReference] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    if (!reference.trim()) { setErr('Discharge reference is required.'); return; }
    setBusy(true); setErr(null);
    try {
      await dischargeCollateralCharge(item.id, reference.trim());
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Discharge failed.');
    } finally { setBusy(false); }
  }

  return (
    <ModalShell
      title="Discharge legal charge"
      subtitle="Records the registry's release filing. The collateral row stays — only the charge cycle closes."
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !reference.trim()} onClick={() => void save()}>
            {busy ? 'Filing…' : '🔓 Confirm discharge'}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>}

      <div className="muted tiny" style={{
        padding: 10,
        background: 'var(--info-bg, #eaf3fb)',
        color: 'var(--info-fg, #0b5394)',
        borderRadius: 6,
        marginBottom: 14,
      }}>
        Currently registered with <strong>{item.charge_registry ? CHARGE_REGISTRY_META[item.charge_registry as ChargeRegistry]?.label : '—'}</strong>{' '}
        as <span className="mono">{item.charge_reference}</span>{' '}
        (filed {item.charge_registered_at?.slice(0, 10)}).
      </div>

      <FormSection label="Discharge reference" hint="The registry's release / discharge filing number.">
        <input
          className="input"
          value={reference}
          onChange={(e) => setReference(e.target.value)}
          placeholder="e.g. DL/2027/00873"
          autoFocus
        />
      </FormSection>
    </ModalShell>
  );
}

// ─────────── Phase 1.5b — Insurance sub-tab ───────────

function InsuranceSubTab({ collateralId, currency, onChanged }: { collateralId: string; currency: string; onChanged: () => void }) {
  const [items, setItems] = useState<CollateralInsurancePolicy[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  async function load() {
    setErr(null);
    try { const r = await getCollateralInsuranceHistory(collateralId); setItems(r.items); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.'); }
  }
  useEffect(() => { void load(); }, [collateralId]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (items === null) return <div className="muted">Loading…</div>;
  const current = items.find((p) => p.is_current);

  return (
    <div>
      {current && (
        <div className="card" style={{ padding: 10, marginBottom: 10 }}>
          <div style={{ fontWeight: 600 }}>Current policy</div>
          <div className="muted tiny">{current.provider_name} · {current.policy_no}</div>
          <div className="tiny">Valid {current.effective_from} → {current.effective_to}</div>
          <div className="tiny">Sum insured: {currency} {fmtKES(current.sum_insured)}</div>
          <div className="tiny" style={{ color: current.status === 'expired' ? 'var(--danger-fg, #b42318)' : undefined }}>
            Status: {current.status}
          </div>
        </div>
      )}
      {!adding && (
        <button className="btn btn-sm btn-primary" onClick={() => setAdding(true)}>+ Record insurance policy</button>
      )}
      {adding && <InsuranceForm collateralId={collateralId} onCancel={() => setAdding(false)} onSaved={() => { setAdding(false); void load(); onChanged(); }} />}
      {items.length > 0 && (
        <>
          <div className="muted tiny" style={{ marginTop: 12 }}>History</div>
          <table className="tbl">
            <thead><tr><th>From</th><th>To</th><th>Provider</th><th>Policy no</th><th>Status</th></tr></thead>
            <tbody>
              {items.map((p) => (
                <tr key={p.id}>
                  <td className="tiny mono">{p.effective_from}</td>
                  <td className="tiny mono">{p.effective_to}</td>
                  <td>{p.provider_name}</td>
                  <td className="tiny mono">{p.policy_no}</td>
                  <td className="tiny">{p.status}{p.is_current ? ' · current' : ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}

function InsuranceForm({ collateralId, onCancel, onSaved }: { collateralId: string; onCancel: () => void; onSaved: () => void }) {
  const [provider, setProvider] = useState('');
  const [policyNo, setPolicyNo] = useState('');
  const [from, setFrom] = useState(new Date().toISOString().slice(0, 10));
  const [to, setTo] = useState('');
  const [sumInsured, setSumInsured] = useState('');
  const [premium, setPremium] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    if (!provider || !policyNo || !from || !to || !sumInsured) { setErr('All fields except premium are required.'); return; }
    setBusy(true); setErr(null);
    try {
      await recordCollateralInsurance(collateralId, {
        provider_name: provider, policy_no: policyNo,
        effective_from: from, effective_to: to,
        sum_insured: sumInsured,
        premium_amount: premium || undefined,
      });
      onSaved();
    } catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ marginTop: 10, padding: 10, background: 'var(--surface-2, #f7f7f9)', borderRadius: 6 }}>
      {err && <div className="alert alert-error">{err}</div>}
      <input className="input" placeholder="Provider name" value={provider} onChange={(e) => setProvider(e.target.value)} />
      <input className="input" style={{ marginTop: 6 }} placeholder="Policy number" value={policyNo} onChange={(e) => setPolicyNo(e.target.value)} />
      <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
        <input className="input" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        <input className="input" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
      </div>
      <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
        <input className="input" inputMode="decimal" placeholder="Sum insured" value={sumInsured} onChange={(e) => setSumInsured(e.target.value)} />
        <input className="input" inputMode="decimal" placeholder="Premium (opt)" value={premium} onChange={(e) => setPremium(e.target.value)} />
      </div>
      <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
        <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</button>
        <button className="btn btn-sm" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

// ─────────── Phase 1.5b — Custody sub-tab ───────────

function CustodySubTab({ collateralId, onChanged }: { collateralId: string; onChanged: () => void }) {
  const [items, setItems] = useState<CustodyMovementRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  async function load() {
    setErr(null);
    try { const r = await getCollateralCustodyTimeline(collateralId); setItems(r.items); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.'); }
  }
  useEffect(() => { void load(); }, [collateralId]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (items === null) return <div className="muted">Loading…</div>;
  return (
    <div>
      {!adding && <button className="btn btn-sm btn-primary" onClick={() => setAdding(true)}>+ Record movement</button>}
      {adding && <CustodyForm collateralId={collateralId} onCancel={() => setAdding(false)} onSaved={() => { setAdding(false); void load(); onChanged(); }} />}
      {items.length === 0 ? <div className="muted" style={{ marginTop: 10 }}>No custody events yet.</div> : (
        <table className="tbl" style={{ marginTop: 10 }}>
          <thead><tr><th>When</th><th>Document</th><th>Movement</th><th>Location</th></tr></thead>
          <tbody>
            {items.map((m) => (
              <tr key={m.id}>
                <td className="tiny">{new Date(m.movement_at).toLocaleString()}</td>
                <td>{m.document_kind}</td>
                <td className="tiny">{m.movement.replace(/_/g, ' ')}</td>
                <td className="tiny mono">{m.location_code ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function CustodyForm({ collateralId, onCancel, onSaved }: { collateralId: string; onCancel: () => void; onSaved: () => void }) {
  const [documentKind, setDocumentKind] = useState('original_title');
  const [movement, setMovement] = useState<CustodyMovement>('checked_out');
  const [location, setLocation] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    if (!documentKind) { setErr('Document kind required.'); return; }
    setBusy(true); setErr(null);
    try {
      await recordCollateralCustody(collateralId, {
        document_kind: documentKind, movement,
        location_code: location || undefined,
        notes: notes || undefined,
      });
      onSaved();
    } catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ marginTop: 10, padding: 10, background: 'var(--surface-2, #f7f7f9)', borderRadius: 6 }}>
      {err && <div className="alert alert-error">{err}</div>}
      <input className="input" placeholder="Document kind (e.g. original_title)" value={documentKind} onChange={(e) => setDocumentKind(e.target.value)} />
      <select className="input" style={{ marginTop: 6 }} value={movement} onChange={(e) => setMovement(e.target.value as CustodyMovement)}>
        <option value="checked_in">Checked in (returned to vault)</option>
        <option value="checked_out">Checked out (taken from vault)</option>
        <option value="returned_to_borrower">Returned to borrower (terminal)</option>
      </select>
      <input className="input" style={{ marginTop: 6 }} placeholder="Location code (e.g. HQ_VAULT_2A)" value={location} onChange={(e) => setLocation(e.target.value)} />
      <textarea className="input" style={{ marginTop: 6 }} rows={2} placeholder="Notes" value={notes} onChange={(e) => setNotes(e.target.value)} />
      <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
        <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</button>
        <button className="btn btn-sm" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

// ─────────── Phase 1.5b — Auction sub-tab ───────────

function AuctionSubTab({ item, currency, onChanged }: { item: LoanCollateralItem; currency: string; onChanged: () => void }) {
  const { hasPermission } = useAuth();
  const collateralId = item.id;
  const status = item.status;
  const canAuction = hasPermission('loans:auction');
  const [items, setItems] = useState<AuctionEventRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [markOpen, setMarkOpen] = useState(false);

  async function load() {
    setErr(null);
    try { const r = await getCollateralAuctionEvents(collateralId); setItems(r.items); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Load failed.'); }
  }
  useEffect(() => { void load(); }, [collateralId]);

  if (err) return <div className="alert alert-error">{err}</div>;
  if (items === null) return <div className="muted">Loading…</div>;

  // Status-driven empty states + call-to-action so officers know what
  // to do next from the sub-tab itself.
  if (status === 'pledged' && items.length === 0) {
    return (
      <div>
        <div className="card" style={{
          padding: 16,
          background: 'var(--warning-bg, #fff4d6)',
          color: 'var(--warning-fg, #8a6d00)',
          marginBottom: 12,
        }}>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>⚠ This collateral is still pledged.</div>
          <div className="tiny" style={{ marginBottom: 10 }}>
            Auction events can only be recorded after marking the collateral as auctioned. This is
            a terminal status — make sure the loan has defaulted and disposal has been authorised
            before continuing.
          </div>
          {canAuction ? (
            <button
              className="btn btn-sm"
              style={{ background: 'var(--danger-fg, #b42318)', color: 'white' }}
              onClick={() => setMarkOpen(true)}
            >Mark as auctioned</button>
          ) : (
            <div className="tiny">You don&rsquo;t have <code>loans:auction</code>.</div>
          )}
        </div>
        {markOpen && (
          <MarkAuctionedModal
            item={item}
            onClose={() => setMarkOpen(false)}
            onSaved={() => { setMarkOpen(false); onChanged(); }}
          />
        )}
      </div>
    );
  }
  if (status !== 'auctioned' && items.length === 0) {
    return (
      <div className="muted">
        Auction events only apply once the collateral is auctioned. Currently <strong>{status}</strong>.
      </div>
    );
  }

  return (
    <div>
      {!adding && <button className="btn btn-sm btn-primary" onClick={() => setAdding(true)}>+ Record auction event</button>}
      {adding && <AuctionForm collateralId={collateralId} onCancel={() => setAdding(false)} onSaved={() => { setAdding(false); void load(); onChanged(); }} />}
      {items.length > 0 && (
        <table className="tbl" style={{ marginTop: 10 }}>
          <thead><tr><th>When</th><th>Event</th><th className="num">Amount</th><th>Buyer / auctioneer</th></tr></thead>
          <tbody>
            {items.map((e) => (
              <tr key={e.id}>
                <td className="tiny">{new Date(e.occurred_at).toLocaleString()}</td>
                <td className="tiny">{e.event_kind.replace(/_/g, ' ')}</td>
                <td className="num mono">{e.amount ? `${currency} ${fmtKES(e.amount)}` : '—'}</td>
                <td className="tiny">{e.buyer_details || e.auctioneer_name || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ─────────── Mark-auctioned modal ───────────

function MarkAuctionedModal({ item, onClose, onSaved }: {
  item: LoanCollateralItem;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [reason, setReason] = useState('');
  const [acknowledge, setAcknowledge] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const canSave = reason.trim().length > 0 && acknowledge;

  async function save() {
    if (!canSave) { setErr('Confirm the acknowledgement and add a reason.'); return; }
    setBusy(true); setErr(null);
    try {
      await markCollateralAuctioned(item.id, reason.trim());
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.');
    } finally { setBusy(false); }
  }

  return (
    <ModalShell
      title="Mark collateral as auctioned"
      subtitle="This is a terminal status — the collateral cannot be unmarked. Use this once disposal has been authorised."
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button
            className="btn btn-danger"
            disabled={busy || !canSave}
            onClick={() => void save()}
          >{busy ? 'Marking…' : 'Mark as auctioned'}</button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 14 }}>{err}</div>}

      <div style={{
        padding: 12,
        marginBottom: 14,
        background: 'var(--danger-bg, #fdecea)',
        color: 'var(--danger-fg, #b42318)',
        borderRadius: 8,
        display: 'flex', gap: 12, alignItems: 'flex-start',
      }}>
        <div style={{ fontSize: 22 }}>⚠</div>
        <div className="tiny" style={{ lineHeight: 1.5 }}>
          <strong>What happens when you confirm:</strong>
          <ul style={{ marginTop: 6, paddingLeft: 18 }}>
            <li>The collateral status flips to <strong>auctioned</strong> (terminal).</li>
            <li>Any backing internal lien / share pledge is <strong>released</strong>.</li>
            <li>You unlock the <strong>Auction events</strong> sub-tab to record the handover, sale, and proceeds.</li>
            <li>Proceeds from the auction are applied via the loan repayment endpoint with channel <code>auction_proceeds</code>.</li>
          </ul>
        </div>
      </div>

      <FormSection
        label="Reason"
        hint="Internal note — e.g. 'Loan defaulted, board approved disposal on 2026-04-12'."
      >
        <textarea
          className="input"
          rows={3}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Why is this collateral going to auction?"
          autoFocus
        />
      </FormSection>

      <label style={{
        display: 'flex', gap: 10, alignItems: 'flex-start',
        padding: 10,
        border: `2px solid ${acknowledge ? 'var(--danger-fg, #b42318)' : 'var(--border, #eee)'}`,
        borderRadius: 8, cursor: 'pointer',
        background: acknowledge ? 'var(--surface-2, #fafafa)' : undefined,
      }}>
        <input
          type="checkbox"
          checked={acknowledge}
          onChange={(e) => setAcknowledge(e.target.checked)}
        />
        <span className="tiny">
          I understand this status change is <strong>terminal</strong> and will free any internal lien
          on <strong>{item.description}</strong>.
        </span>
      </label>
    </ModalShell>
  );
}

function AuctionForm({ collateralId, onCancel, onSaved }: { collateralId: string; onCancel: () => void; onSaved: () => void }) {
  const [eventKind, setEventKind] = useState<AuctionEventKind>('handover_to_auctioneer');
  const [amount, setAmount] = useState('');
  const [auctioneer, setAuctioneer] = useState('');
  const [buyer, setBuyer] = useState('');
  const [notes, setNotes] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const needsAmount = eventKind === 'sold' || eventKind === 'proceeds_received';
  async function save() {
    if (needsAmount && !amount) { setErr('Amount required for this event.'); return; }
    setBusy(true); setErr(null);
    try {
      await recordCollateralAuctionEvent(collateralId, {
        event_kind: eventKind,
        amount: amount || undefined,
        auctioneer_name: auctioneer || undefined,
        buyer_details: buyer || undefined,
        notes: notes || undefined,
      });
      onSaved();
    } catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Save failed.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ marginTop: 10, padding: 10, background: 'var(--surface-2, #f7f7f9)', borderRadius: 6 }}>
      {err && <div className="alert alert-error">{err}</div>}
      <select className="input" value={eventKind} onChange={(e) => setEventKind(e.target.value as AuctionEventKind)}>
        <option value="handover_to_auctioneer">Handover to auctioneer</option>
        <option value="auction_notice_published">Auction notice published</option>
        <option value="auction_held">Auction held</option>
        <option value="sold">Sold</option>
        <option value="reserve_not_met">Reserve not met</option>
        <option value="rescheduled">Rescheduled</option>
        <option value="proceeds_received">Proceeds received</option>
      </select>
      {needsAmount && (
        <input className="input" style={{ marginTop: 6 }} inputMode="decimal" placeholder="Amount (KES)" value={amount} onChange={(e) => setAmount(e.target.value)} />
      )}
      <input className="input" style={{ marginTop: 6 }} placeholder="Auctioneer name (optional)" value={auctioneer} onChange={(e) => setAuctioneer(e.target.value)} />
      <input className="input" style={{ marginTop: 6 }} placeholder="Buyer details (sold/proceeds)" value={buyer} onChange={(e) => setBuyer(e.target.value)} />
      <textarea className="input" style={{ marginTop: 6 }} rows={2} placeholder="Notes" value={notes} onChange={(e) => setNotes(e.target.value)} />
      <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
        <button className="btn btn-primary btn-sm" disabled={busy} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</button>
        <button className="btn btn-sm" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

// helper to keep useMemo import non-dead (it's already used by Add modal further up).
const _useMemo = useMemo;
void _useMemo;
