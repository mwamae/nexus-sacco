// Loans Phase 5 — Salary check-off batches at /loans/checkoff.
//
// Lists historical batches with status. Upload button posts a new
// batch (multipart). Click into a batch → review + validate + post.

import { useEffect, useState } from 'react';
import {
  uploadCheckoffBatch,
  listCheckoffBatches,
  getCheckoffBatch,
  validateCheckoffBatch,
  postCheckoffBatch,
  resolveCheckoffRow,
  type CheckoffBatch,
  type CheckoffBatchRow,
  extractError,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const STATUS_COLOR: Record<string, string> = {
  draft: 'var(--muted)',
  validated: '#3b6ab8',
  posted: 'var(--pos)',
  partial: 'var(--warn)',
  failed: 'var(--neg)',
  cancelled: 'var(--muted)',
};

const ROW_STATUS_COLOR: Record<string, string> = {
  pending: 'var(--muted)',
  matched: 'var(--pos)',
  ambiguous: 'var(--warn)',
  unmatched: 'var(--neg)',
  posted: 'var(--pos)',
  failed: 'var(--neg)',
  skipped: 'var(--muted)',
};

export default function CheckoffPage() {
  useDocumentTitle('Loans · Salary check-off');
  const { hasPermission } = useAuth();
  const canUpload = hasPermission('loans:checkoff:upload');
  const canPost = hasPermission('loans:checkoff:post');

  const [batches, setBatches] = useState<CheckoffBatch[]>([]);
  const [selected, setSelected] = useState<{ batch: CheckoffBatch; rows: CheckoffBatchRow[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);

  async function reload() {
    setErr(null);
    try { setBatches((await listCheckoffBatches()).items); }
    catch (e) { setErr(extractError(e)); }
  }
  useEffect(() => { void reload(); }, []);

  async function openBatch(id: string) {
    try { setSelected(await getCheckoffBatch(id)); }
    catch (e) { setErr(extractError(e)); }
  }

  async function doValidate(id: string) {
    setBusy(true); setErr(null);
    try {
      await validateCheckoffBatch(id);
      await reload(); await openBatch(id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function doPost(id: string) {
    if (!confirm('Post all matched rows to the ledger? This creates loan repayment JEs.')) return;
    setBusy(true); setErr(null);
    try {
      await postCheckoffBatch(id);
      await reload(); await openBatch(id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  async function skipRow(batchID: string, rowID: string) {
    if (!confirm('Skip this row? It will not be posted.')) return;
    setBusy(true);
    try {
      await resolveCheckoffRow(batchID, rowID, { skip: true });
      await openBatch(batchID);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Loans · Operations</div>
          <h1>Salary check-off</h1>
          <div className="page-sub">
            Bulk-post loan repayments from a payroll deduction file. Upload a CSV
            with <code>member_no,amount</code>, validate to resolve matches, then
            post the batch — each matched row creates a repayment txn on the
            member's active loan.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div style={{ display: 'flex', gap: 12, marginTop: 12 }}>
        <button className="btn btn-primary" disabled={!canUpload} onClick={() => setUploadOpen(true)}>
          + Upload batch
        </button>
        <button className="btn" onClick={() => void reload()}>↻ Refresh</button>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(340px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
        <div className="card">
          <div className="card-hd"><h3>History</h3><span className="card-sub">{batches.length} batch{batches.length === 1 ? '' : 'es'}</span></div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Period</th><th>Employer</th><th>Status</th><th className="num">Rows</th></tr></thead>
              <tbody>
                {batches.length === 0 ? (
                  <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 18 }}>No batches yet.</td></tr>
                ) : batches.map((b) => (
                  <tr key={b.id} style={{ cursor: 'pointer', background: selected?.batch.id === b.id ? 'var(--surface-2)' : undefined }} onClick={() => void openBatch(b.id)}>
                    <td className="muted tiny">{b.period_label}</td>
                    <td>{b.employer_name}</td>
                    <td><span style={{ color: STATUS_COLOR[b.status], fontWeight: 600 }}>{b.status}</span></td>
                    <td className="num mono">{b.matched_count}/{b.row_count}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-hd">
            <h3>{selected ? selected.batch.employer_name + ' · ' + selected.batch.period_label : 'Detail'}</h3>
            {selected && <span className="card-sub" style={{ color: STATUS_COLOR[selected.batch.status] }}>{selected.batch.status}</span>}
          </div>
          <div className="card-body" style={{ display: 'grid', gap: 12 }}>
            {!selected && <div className="muted">Pick a batch from the list.</div>}
            {selected && (
              <>
                <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
                  <KPI label="Rows" value={String(selected.batch.row_count)} />
                  <KPI label="Matched" value={String(selected.batch.matched_count)} tone="pos" />
                  <KPI label="Unmatched" value={String(selected.batch.unmatched_count)} tone="warn" />
                  <KPI label="Posted amount" value={selected.batch.posted_amount} mono />
                </div>
                <div style={{ display: 'flex', gap: 8 }}>
                  {(selected.batch.status === 'draft' || selected.batch.status === 'validated') && (
                    <button className="btn" disabled={busy || !canUpload} onClick={() => void doValidate(selected.batch.id)}>
                      {busy ? '…' : '↻ Re-validate'}
                    </button>
                  )}
                  {(selected.batch.status === 'validated' || selected.batch.status === 'partial') && (
                    <button className="btn btn-primary" disabled={busy || !canPost} onClick={() => void doPost(selected.batch.id)}>
                      {busy ? 'Posting…' : 'Post batch'}
                    </button>
                  )}
                </div>
                <div className="card-body flush">
                  <table className="tbl">
                    <thead>
                      <tr>
                        <th>#</th><th>Member no</th><th className="num">Amount</th>
                        <th>Status</th><th>Note</th><th></th>
                      </tr>
                    </thead>
                    <tbody>
                      {selected.rows.map((r) => (
                        <tr key={r.id}>
                          <td className="mono">{r.row_no}</td>
                          <td className="mono">{r.member_no_raw}</td>
                          <td className="num mono">{r.amount ?? r.amount_raw}</td>
                          <td><span style={{ color: ROW_STATUS_COLOR[r.status], fontWeight: 600 }}>{r.status}</span></td>
                          <td className="muted tiny">{r.error_message ?? '—'}</td>
                          <td>
                            {(r.status === 'unmatched' || r.status === 'ambiguous') && (
                              <button className="btn btn-sm" disabled={busy || !canUpload}
                                onClick={() => void skipRow(selected.batch.id, r.id)}>Skip</button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            )}
          </div>
        </div>
      </div>

      {uploadOpen && (
        <UploadModal
          onClose={() => setUploadOpen(false)}
          onUploaded={async (batchID) => {
            setUploadOpen(false);
            await reload();
            await openBatch(batchID);
          }}
        />
      )}
    </div>
  );
}

function UploadModal({ onClose, onUploaded }: { onClose: () => void; onUploaded: (id: string) => void }) {
  const [employer, setEmployer] = useState('');
  const [period, setPeriod] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    if (!file) { setErr('Please select a file'); return; }
    setBusy(true); setErr(null);
    try {
      const form = new FormData();
      form.set('employer_name', employer);
      form.set('period_label', period);
      form.set('file', file);
      const r = await uploadCheckoffBatch(form);
      onUploaded(r.batch_id);
    } catch (e) { setErr(extractError(e)); }
    finally { setBusy(false); }
  }
  return (
    <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000 }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, width: '90%', maxWidth: 520, padding: 20 }}>
        <h3 style={{ marginTop: 0 }}>Upload check-off batch</h3>
        <label><div className="muted tiny">Employer name</div>
          <input className="input" value={employer} onChange={(e) => setEmployer(e.target.value)} />
        </label>
        <label><div className="muted tiny">Period label</div>
          <input className="input" value={period} onChange={(e) => setPeriod(e.target.value)} placeholder="e.g. 2026-06" />
        </label>
        <label><div className="muted tiny">CSV file</div>
          <input type="file" accept=".csv" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        </label>
        <p className="muted tiny" style={{ marginTop: 8 }}>
          Format: <code>member_no,amount</code> per line. Header row optional.
        </p>
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || !employer || !period || !file} onClick={() => void submit()}>
            {busy ? 'Uploading…' : 'Upload'}
          </button>
        </div>
      </div>
    </div>
  );
}

function KPI({ label, value, tone, mono }: { label: string; value: string; tone?: 'pos' | 'warn' | 'neg'; mono?: boolean }) {
  const color = tone === 'pos' ? 'var(--pos)' : tone === 'warn' ? 'var(--warn)' : tone === 'neg' ? 'var(--neg)' : 'var(--fg)';
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{ fontSize: 20, fontWeight: 700, color, fontFamily: mono ? 'var(--font-mono)' : undefined }}>{value}</div>
    </div>
  );
}
