// /loans/reports/sasra — SASRA quarterly extract.
//
// Phase 2 ships the extract with explicit safety nets:
//
//   1. DRAFT watermark on every CSV/PDF until the tenant admin
//      verifies the column layout matches the current SASRA Form D.
//   2. First-use banner explaining the verification flow.
//   3. Period picker (current quarter default).
//   4. Three actions: Preview (JSON), Download CSV, Download PDF.
//   5. Verification flow — opens a small form capturing the form
//      version + optional note. POSTs to /v1/loans/reports/sasra/verify.
//
// Per-tenant column layout overrides land at Settings → Loans
// policy → SASRA layout (deferred to a follow-up — Phase 2 ships
// the default from sasra_columns.json).
//
// Permission: loans:sasra.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import { getSASRAExtract, sasraDownloadURL, verifySASRAPeriod, extractError } from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

function currentQuarter(): string {
  const now = new Date();
  const q = Math.floor(now.getUTCMonth() / 3) + 1;
  return `${now.getUTCFullYear()}-Q${q}`;
}

function quarterOptions(): string[] {
  // Last 8 quarters incl. current.
  const out: string[] = [];
  const now = new Date();
  for (let i = 0; i < 8; i++) {
    const d = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i * 3, 1));
    const q = Math.floor(d.getUTCMonth() / 3) + 1;
    out.push(`${d.getUTCFullYear()}-Q${q}`);
  }
  return Array.from(new Set(out));
}

export default function SASRAPage() {
  useDocumentTitle('Loans · SASRA quarterly extract');
  const { hasPermission } = useAuth();
  const allowed = hasPermission('loans:sasra');
  const [period, setPeriod] = useState(currentQuarter());
  const [preview, setPreview] = useState<any>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [showVerify, setShowVerify] = useState(false);

  const quarters = useMemo(() => quarterOptions(), []);

  const load = useCallback(async () => {
    setBusy(true);
    setErr(null);
    try {
      const data = await getSASRAExtract(period);
      setPreview(data);
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }, [period]);

  useEffect(() => { if (allowed) void load(); }, [allowed, load]);

  if (!allowed) {
    return (
      <div className="page">
        <div className="page-hd"><h1>SASRA quarterly extract</h1></div>
        <div className="alert alert-warn">
          You need <code>loans:sasra</code> permission. This page is restricted to
          sacco_admin and auditor by default.
        </div>
      </div>
    );
  }

  const verified = !!preview?.verified;
  const rowCount = preview?.rows?.length ?? 0;
  const columns = preview?.columns ?? [];

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">
            <a href="/loans/reports" style={{ color: 'inherit' }}>← Reports</a>
          </div>
          <h1>SASRA quarterly extract</h1>
          <div className="page-sub">
            Generates the loan-portfolio CSV the SACCO uploads to the regulator's portal,
            plus a PDF management report.
          </div>
        </div>
      </div>

      {/* First-use banner — shown when this period has never been verified. */}
      {!verified && (
        <div className="alert alert-warn" style={{ marginBottom: 12 }}>
          <strong>DRAFT — verify column layout before submission.</strong>
          <div style={{ marginTop: 6 }}>
            The extract ships with a DRAFT watermark until the column layout is verified
            against the current SASRA Form D. Compare the first generated CSV against
            your last manually-prepared submission. If the columns don't match, request
            a per-tenant layout override from Settings → Loans policy → SASRA layout.
          </div>
          <div style={{ marginTop: 8 }}>
            <button className="btn btn-sm btn-accent" onClick={() => setShowVerify(true)}>
              Mark column layout verified for {period}
            </button>
          </div>
        </div>
      )}

      {verified && (
        <div className="alert alert-info" style={{ marginBottom: 12 }}>
          ✓ Column layout verified for {period}{' '}
          {preview.verification?.verified_form_version && (
            <>({preview.verification.verified_form_version})</>
          )}.
          Downloads no longer carry the DRAFT watermark.
        </div>
      )}

      <div className="card" style={{ marginBottom: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
          <label>
            <div className="muted tiny" style={{ marginBottom: 4 }}>Period</div>
            <select className="input" value={period} onChange={(e) => setPeriod(e.target.value)}>
              {quarters.map((q) => <option key={q} value={q}>{q}</option>)}
            </select>
          </label>
          <button className="btn" onClick={() => void load()} disabled={busy}>
            {busy ? 'Loading…' : '↻ Preview'}
          </button>
          <a className="btn btn-accent" href={sasraDownloadURL(period, 'csv')}>Download CSV</a>
          <a className="btn" href={sasraDownloadURL(period, 'pdf')}>Download PDF</a>
        </div>
      </div>

      {err && <div className="alert alert-error">{err}</div>}

      {preview && (
        <>
          <div className="card" style={{ marginBottom: 12 }}>
            <div className="card-hd">
              <h3>Preview — {rowCount} loan{rowCount === 1 ? '' : 's'}</h3>
              <span className="card-sub">
                Format: {preview.format_version}
                {!verified && <strong style={{ color: 'var(--neg)', marginLeft: 8 }}>· DRAFT</strong>}
              </span>
            </div>
            <div className="card-body" style={{ overflow: 'auto', maxHeight: 480 }}>
              {rowCount === 0 ? (
                <div className="empty">No loans in the selected period.</div>
              ) : (
                <table className="tbl">
                  <thead>
                    <tr>{columns.map((c: any) => <th key={c.key}>{c.label}</th>)}</tr>
                  </thead>
                  <tbody>
                    {preview.rows.slice(0, 50).map((row: any, i: number) => (
                      <tr key={i}>
                        {columns.map((c: any) => (
                          <td key={c.key} className={c.type === 'decimal' || c.type === 'integer' ? 'num mono' : ''}>
                            {String(row[c.key] ?? '')}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          </div>

          {/* Header metadata preview */}
          <div className="card">
            <div className="card-hd"><h3>Header metadata</h3></div>
            <div className="card-body">
              {Object.entries(preview.header_metadata ?? {}).map(([k, v]: any) => (
                <div key={k} style={{ display: 'flex', gap: 8, padding: '2px 0' }}>
                  <span className="muted tiny mono" style={{ width: 220 }}>{k}</span>
                  <span>{v}</span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}

      {showVerify && (
        <VerifyModal
          period={period}
          onClose={() => setShowVerify(false)}
          onSaved={() => { setShowVerify(false); void load(); }}
        />
      )}
    </div>
  );
}

function VerifyModal({ period, onClose, onSaved }: { period: string; onClose: () => void; onSaved: () => void }) {
  const [formVersion, setFormVersion] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setBusy(true); setErr(null);
    try {
      await verifySASRAPeriod(period, formVersion, note || undefined);
      onSaved();
    } catch (e) { setErr(extractError(e)); } finally { setBusy(false); }
  }
  return (
    <div role="dialog" aria-modal="true" style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div style={{ background: 'var(--surface)', borderRadius: 8, maxWidth: 540, width: '90%', padding: 20 }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>Verify column layout for {period}</h3>
          <button className="btn btn-sm btn-ghost" onClick={onClose}>×</button>
        </div>
        <p className="muted tiny">
          By verifying you confirm the column layout in the generated CSV matches
          the SASRA form revision named below. Subsequent quarters re-use this
          verification until the form version changes.
        </p>
        <label>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Verified form version</div>
          <input
            className="input"
            type="text"
            placeholder="e.g. SASRA Form D 2025-Q4 revision"
            value={formVersion}
            onChange={(e) => setFormVersion(e.target.value)}
          />
        </label>
        <label style={{ display: 'block', marginTop: 8 }}>
          <div className="muted tiny" style={{ marginBottom: 4 }}>Note (optional)</div>
          <textarea
            className="input"
            rows={3}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="e.g. verified against PDF circulated 2026-03-15 via SASRA email"
          />
        </label>
        {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
          <button className="btn" onClick={onClose}>Cancel</button>
          <button className="btn btn-accent" disabled={busy || !formVersion} onClick={() => void submit()}>
            {busy ? 'Saving…' : 'Verify'}
          </button>
        </div>
      </div>
    </div>
  );
}
