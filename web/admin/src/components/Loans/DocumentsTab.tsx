// Phase-1 follow-up — Documents tab shared between the application
// detail page (pre-disbursement) and the loan detail page (post-
// disbursement).
//
// Three regions:
//   A — Required documents checklist (only when product has required kinds)
//   B — All documents list, with toggle-able history
//   C — Action bar — Upload + Download bundle

import { useEffect, useMemo, useState } from 'react';
import {
  listApplicationDocuments,
  listLoanDocuments,
  loanDocumentDownloadURL,
  loanApplicationBundleURL,
  loanBundleURL,
  uploadApplicationDocument,
  uploadLoanDocument,
  reviewLoanDocument,
  deleteLoanDocument,
  getRequiredDocsStatus,
  type LoanDocumentRow,
  type RequiredDocsStatusResp,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = {
  applicationId?: string;
  loanId?: string;
  readOnly?: boolean;
  onChanged?: () => void;
};

const DOC_KINDS = [
  'id_copy', 'payslip', 'bank_statement', 'mpesa_statement',
  'business_financials', 'guarantor_consent_proof',
  'offer_letter_signed', 'agreement',
  'other',
] as const;

const KIND_LABEL: Record<string, string> = {
  id_copy: 'ID copy',
  payslip: 'Payslip',
  bank_statement: 'Bank statement',
  mpesa_statement: 'M-Pesa statement',
  business_financials: 'Business financials',
  guarantor_consent_proof: 'Guarantor consent proof',
  offer_letter_signed: 'Signed offer letter',
  agreement: 'Loan agreement',
  other: 'Other',
};

function humanKind(k: string): string {
  return KIND_LABEL[k] ?? k.replace(/_/g, ' ');
}

function relative(ts: string): string {
  const d = new Date(ts);
  const diffMs = Date.now() - d.getTime();
  const day = 24 * 60 * 60 * 1000;
  if (diffMs < day) return 'today';
  if (diffMs < 2 * day) return '1 day ago';
  const days = Math.floor(diffMs / day);
  if (days < 30) return `${days} days ago`;
  if (days < 60) return '1 mo ago';
  return `${Math.floor(days / 30)} mo ago`;
}

export default function DocumentsTab({ applicationId, loanId, readOnly, onChanged }: Props) {
  const { hasPermission } = useAuth();
  const [items, setItems] = useState<LoanDocumentRow[]>([]);
  const [required, setRequired] = useState<RequiredDocsStatusResp | null>(null);
  const [includeHistory, setIncludeHistory] = useState(false);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [uploadOpen, setUploadOpen] = useState<{ kind?: string } | null>(null);
  const [reviewItem, setReviewItem] = useState<LoanDocumentRow | null>(null);

  async function refresh() {
    setLoading(true);
    setErr(null);
    try {
      const docsResp = applicationId
        ? await listApplicationDocuments(applicationId, includeHistory)
        : loanId
          ? await listLoanDocuments(loanId, includeHistory)
          : { items: [], total: 0 };
      setItems(docsResp.items);
      if (applicationId) {
        try { setRequired(await getRequiredDocsStatus(applicationId)); }
        catch { setRequired(null); }
      }
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Failed to load documents.');
    } finally { setLoading(false); }
  }
  useEffect(() => { void refresh(); }, [applicationId, loanId, includeHistory]);

  const bundleURL = applicationId ? loanApplicationBundleURL(applicationId) : (loanId ? loanBundleURL(loanId) : '');

  return (
    <div>
      {err && <div className="alert alert-error" style={{ marginBottom: 8 }}>{err}</div>}

      {/* Region A — required-docs checklist. Backend may return
          `required: null` when the product has no required kinds — guard
          with the nullish-coalescing so length-of-null doesn't throw. */}
      {required && (required.required ?? []).length > 0 && (
        <RequiredDocsChecklist
          data={required}
          readOnly={readOnly}
          onUpload={(kind) => setUploadOpen({ kind })}
        />
      )}

      {/* Region B — full list */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 14, marginBottom: 6 }}>
        <h3 style={{ margin: 0 }}>All documents</h3>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <label style={{ display: 'flex', gap: 4, fontSize: 12 }}>
            <input type="checkbox" checked={includeHistory} onChange={(e) => setIncludeHistory(e.target.checked)} />
            Show history
          </label>
          {!readOnly && items.length > 0 && (
            <a className="btn btn-sm" href={bundleURL} download>Download bundle (PDF)</a>
          )}
          {!readOnly && hasPermission('loans:apply') && (
            <button className="btn btn-sm btn-primary" onClick={() => setUploadOpen({})}>+ Upload document</button>
          )}
        </div>
      </div>

      {loading ? (
        <div className="muted">Loading…</div>
      ) : items.length === 0 ? (
        <div className="empty" style={{ padding: 14 }}>No documents on file.</div>
      ) : (
        <table className="tbl">
          <thead>
            <tr>
              <th>Kind</th>
              <th>Description</th>
              <th>Status</th>
              <th>Expires</th>
              <th>Uploaded</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {items.map((d) => (
              <tr key={d.id} style={d.is_current ? {} : { opacity: 0.5 }}>
                <td>{humanKind(d.kind)}</td>
                <td>{d.description ?? '—'}</td>
                <td><ReviewPill d={d} /></td>
                <td><ExpiryCell d={d} /></td>
                <td className="tiny">{relative(d.uploaded_at)}</td>
                <td>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <a className="btn btn-sm" href={loanDocumentDownloadURL(d.id)} download>Download</a>
                    {!readOnly && hasPermission('loans:apply') && d.review_status === 'pending' && (
                      <button className="btn btn-sm" onClick={() => setReviewItem(d)}>Review</button>
                    )}
                    {!readOnly && hasPermission('loans:apply') && d.is_current && (
                      <DeleteButton id={d.id} onDeleted={() => { void refresh(); onChanged?.(); }} />
                    )}
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {uploadOpen && (
        <UploadModal
          applicationId={applicationId}
          loanId={loanId}
          presetKind={uploadOpen.kind}
          onClose={() => setUploadOpen(null)}
          onSaved={() => { setUploadOpen(null); void refresh(); onChanged?.(); }}
        />
      )}
      {reviewItem && (
        <ReviewModal
          doc={reviewItem}
          onClose={() => setReviewItem(null)}
          onSaved={() => { setReviewItem(null); void refresh(); onChanged?.(); }}
        />
      )}
    </div>
  );
}

function RequiredDocsChecklist({ data, readOnly, onUpload }: { data: RequiredDocsStatusResp; readOnly?: boolean; onUpload: (kind: string) => void }) {
  return (
    <div className="card" style={{ padding: 12, marginBottom: 10 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline' }}>
        <div style={{ fontWeight: 600 }}>Required documents</div>
        <div className="muted tiny">{data.summary}</div>
      </div>
      <div style={{ marginTop: 8 }}>
        {(data.required ?? []).map((kind) => {
          const s = (data.status ?? {})[kind];
          if (!s) return null;
          let icon = '✓';
          let color = 'var(--success-fg, #146c43)';
          if (!s.satisfied) { icon = '✗'; color = 'var(--danger-fg, #b42318)'; }
          else if (s.warning) { icon = '⚠'; color = 'var(--warning-fg, #8a6d00)'; }
          return (
            <div key={kind} style={{
              display: 'grid', gridTemplateColumns: 'auto 1fr auto auto',
              gap: 10, padding: '4px 0',
              borderBottom: '1px solid var(--border, #eee)',
            }}>
              <span style={{ color, fontWeight: 700 }}>{icon}</span>
              <span>{humanKind(kind)}</span>
              <span className="tiny muted">{s.expires_at ? `expires ${s.expires_at}` : s.reason ? s.reason : ''}</span>
              {!readOnly && !s.satisfied && (
                <button className="btn btn-sm btn-link" onClick={() => onUpload(kind)}>Upload</button>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function ReviewPill({ d }: { d: LoanDocumentRow }) {
  const map: Record<string, { bg: string; fg: string; label: string }> = {
    pending:           { bg: 'var(--warning-bg, #fff4d6)', fg: 'var(--warning-fg, #8a6d00)', label: '⚠ pending' },
    reviewed:          { bg: 'var(--success-bg, #e7f4ec)', fg: 'var(--success-fg, #146c43)', label: '✓ reviewed' },
    needs_replacement: { bg: 'var(--danger-bg, #fdecea)',  fg: 'var(--danger-fg, #b42318)',  label: '↻ replace' },
    flagged:           { bg: 'var(--danger-bg, #fdecea)',  fg: 'var(--danger-fg, #b42318)',  label: '🚩 flagged' },
  };
  const c = map[d.review_status] ?? map.pending;
  return <span style={{ background: c.bg, color: c.fg, padding: '3px 8px', borderRadius: 12, fontSize: 11, fontWeight: 600 }}>{c.label}</span>;
}

function ExpiryCell({ d }: { d: LoanDocumentRow }) {
  if (!d.expires_at) return <span className="tiny muted">—</span>;
  const e = new Date(d.expires_at);
  const days = Math.round((e.getTime() - Date.now()) / (24 * 60 * 60 * 1000));
  if (days < 0) return <span className="tiny" style={{ color: 'var(--danger-fg, #b42318)', fontWeight: 600 }}>EXPIRED {d.expires_at}</span>;
  if (days < 30) return <span className="tiny" style={{ color: 'var(--warning-fg, #8a6d00)' }}>{d.expires_at} ({days}d)</span>;
  return <span className="tiny">{d.expires_at}</span>;
}

function DeleteButton({ id, onDeleted }: { id: string; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      className="btn btn-sm btn-link"
      disabled={busy}
      onClick={async () => {
        if (!window.confirm('Delete this document?')) return;
        setBusy(true);
        try { await deleteLoanDocument(id); onDeleted(); }
        catch (e: any) { alert(e?.response?.data?.error?.message || e?.message || 'Delete failed.'); }
        finally { setBusy(false); }
      }}
    >Delete</button>
  );
}

// ─────────── Modal scaffolding ───────────

function ModalShell({ title, subtitle, onClose, children, footer, maxWidth = 520 }: {
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
        width: `min(92vw, ${maxWidth}px)`,
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
            {subtitle && <div className="muted tiny" style={{ marginTop: 4 }}>{subtitle}</div>}
          </div>
          <button className="btn btn-sm btn-link" onClick={onClose} aria-label="Close" style={{ fontSize: 18, lineHeight: 1, padding: '0 8px' }}>×</button>
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

function FormField({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 14 }}>
      <label className="form-label" style={{ display: 'block', marginBottom: 4, fontSize: 13, fontWeight: 500 }}>{label}</label>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </div>
  );
}

function UploadModal({ applicationId, loanId, presetKind, onClose, onSaved }: {
  applicationId?: string;
  loanId?: string;
  presetKind?: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [kind, setKind] = useState(presetKind ?? 'id_copy');
  const [description, setDescription] = useState('');
  const [expiresAt, setExpiresAt] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    if (!file) { setErr('Choose a file to upload.'); return; }
    setBusy(true); setErr(null);
    try {
      if (applicationId) await uploadApplicationDocument(applicationId, file, kind, description || undefined, expiresAt || undefined);
      else if (loanId)   await uploadLoanDocument(loanId, file, kind, description || undefined, expiresAt || undefined);
      onSaved();
    } catch (e: any) {
      setErr(e?.response?.data?.error?.message || e?.message || 'Upload failed.');
    } finally { setBusy(false); }
  }

  const sizeKB = file ? Math.round(file.size / 1024) : 0;

  return (
    <ModalShell
      title="Upload document"
      subtitle={presetKind ? `Adding required: ${humanKind(presetKind)}` : 'PDF, image, or DOC (10 MB max)'}
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !file} onClick={() => void save()}>
            {busy ? 'Uploading…' : 'Upload document'}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 12 }}>{err}</div>}

      <FormField label="File" hint={file ? `${file.name} · ${sizeKB.toLocaleString()} KB` : 'PDF, JPG/PNG, DOC, DOCX, XLS, XLSX'}>
        <input
          className="input"
          type="file"
          accept=".pdf,image/*,.doc,.docx,.xls,.xlsx"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
      </FormField>

      <FormField label="Document kind">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value)}>
          {DOC_KINDS.map((k) => <option key={k} value={k}>{humanKind(k)}</option>)}
        </select>
      </FormField>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 160px', gap: 12 }}>
        <FormField label="Description" hint="What's in this file — month, period, source.">
          <input
            className="input"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="e.g. June 2026 payslip"
          />
        </FormField>
        <FormField label="Expiry override" hint="Optional. Defaults from tenant config.">
          <input
            className="input"
            type="date"
            value={expiresAt}
            onChange={(e) => setExpiresAt(e.target.value)}
          />
        </FormField>
      </div>
    </ModalShell>
  );
}

function ReviewModal({ doc, onClose, onSaved }: { doc: LoanDocumentRow; onClose: () => void; onSaved: () => void }) {
  const [notes, setNotes] = useState('');
  const [decision, setDecision] = useState<'reviewed' | 'needs_replacement' | 'flagged' | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit() {
    if (!decision) { setErr('Choose a decision.'); return; }
    if ((decision !== 'reviewed') && !notes.trim()) { setErr('Notes are required to flag or request a replacement.'); return; }
    setBusy(true); setErr(null);
    try { await reviewLoanDocument(doc.id, decision, notes || undefined); onSaved(); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Review failed.'); }
    finally { setBusy(false); }
  }

  const DECISION_OPTIONS = [
    { value: 'reviewed',          label: '✓ Approve',          tone: 'success', hint: 'Document is genuine and complete.' },
    { value: 'needs_replacement', label: '↻ Request replacement', tone: 'warning', hint: 'Ask for a fresh document.' },
    { value: 'flagged',           label: '🚩 Flag',             tone: 'danger',  hint: 'Suspicious / requires escalation.' },
  ] as const;

  return (
    <ModalShell
      title="Review document"
      subtitle={`${humanKind(doc.kind)} · ${doc.description ?? 'no description'}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !decision} onClick={() => void submit()}>
            {busy ? 'Submitting…' : 'Submit review'}
          </button>
        </>
      }
    >
      {err && <div className="alert alert-error" style={{ marginBottom: 12 }}>{err}</div>}

      <div style={{ marginBottom: 14, padding: 12, background: 'var(--surface-2, #fafafa)', borderRadius: 8, display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12 }}>
        <div className="muted tiny">Open the file in a new tab to inspect before deciding.</div>
        <a className="btn btn-sm" href={loanDocumentDownloadURL(doc.id)} target="_blank" rel="noreferrer">Open document ↗</a>
      </div>

      <FormField label="Decision">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {DECISION_OPTIONS.map((opt) => {
            const selected = decision === opt.value;
            const toneColor = opt.tone === 'success'
              ? 'var(--success-fg, #146c43)'
              : opt.tone === 'warning'
                ? 'var(--warning-fg, #8a6d00)'
                : 'var(--danger-fg, #b42318)';
            return (
              <label
                key={opt.value}
                style={{
                  display: 'flex', gap: 10, padding: 12,
                  border: `2px solid ${selected ? toneColor : 'var(--border, #eee)'}`,
                  borderRadius: 8, cursor: 'pointer',
                  background: selected ? 'var(--surface-2, #fafafa)' : undefined,
                }}
              >
                <input
                  type="radio"
                  checked={selected}
                  onChange={() => setDecision(opt.value)}
                />
                <div>
                  <div style={{ fontWeight: 600, color: selected ? toneColor : undefined }}>{opt.label}</div>
                  <div className="muted tiny" style={{ marginTop: 2 }}>{opt.hint}</div>
                </div>
              </label>
            );
          })}
        </div>
      </FormField>

      <FormField
        label="Notes"
        hint={decision === 'reviewed' ? 'Optional.' : 'Required — explain what needs to change.'}
      >
        <textarea
          className="input"
          rows={3}
          value={notes}
          onChange={(e) => setNotes(e.target.value)}
          placeholder={decision === 'reviewed' ? 'Anything worth recording…' : 'e.g. Page 2 missing; please re-upload signed copy.'}
        />
      </FormField>
    </ModalShell>
  );
}

function Backdrop({ children, onClose }: { children: React.ReactNode; onClose: () => void }) {
  return (
    <div role="dialog" aria-modal="true" onClick={onClose}
      style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}>
      <div onClick={(e) => e.stopPropagation()}>{children}</div>
    </div>
  );
}

// keep useMemo import alive for future use
const _useMemo = useMemo; void _useMemo;
