// Loans Phase 3 — Loans policy settings page.
//
// Edits two things that drive every classification + provision run:
//
//   1. DPD thresholds (per-tenant)
//      sasra_watch_dpd       — first day a loan is "watch"
//      dpd_substandard_days  — first day "substandard"
//      dpd_doubtful_days     — first day "doubtful"
//      dpd_loss_days         — first day "loss"
//      ifrs9_stage2_dpd      — first day IFRS 9 stage 2 (SICR)
//      ifrs9_stage3_dpd      — first day stage 3 (credit-impaired)
//
//   2. ECL rate matrix (per-tenant, history-preserving)
//      One rate per (SASRA class, IFRS 9 stage) pair. Saving creates
//      a new effective_from=today row only when the value changed —
//      the matrix is its own audit trail.
//
// Permission: loans:policy:write. Held by sacco_admin + tenant_owner +
// platform_admin. The accountant (who runs the monthly cycle) does NOT
// have this perm — segregation of duties: they can't change the rate
// that produces the JE they're about to post.

import { useEffect, useMemo, useState } from 'react';
import { useAuth } from '../../auth/AuthContext';
import {
  getLoansPolicy,
  updateLoansPolicyThresholds,
  updateLoansPolicyMatrix,
  type ECLMatrixRow,
  type LoansPolicyThresholds,
} from '../../api/client';
import { useDocumentTitle } from '../../lib/useDocumentTitle';

const SASRA_CLASSES = ['performing', 'watch', 'substandard', 'doubtful', 'loss'] as const;
const SASRA_LABEL: Record<string, string> = {
  performing: 'Performing',
  watch: 'Watch',
  substandard: 'Substandard',
  doubtful: 'Doubtful',
  loss: 'Loss',
};

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}

export default function LoansPolicyPage() {
  useDocumentTitle('Settings · Loans policy');
  const { hasPermission } = useAuth();
  const canWrite = hasPermission('loans:policy:write');

  const [thresholds, setThresholds] = useState<LoansPolicyThresholds | null>(null);
  const [matrix, setMatrix] = useState<ECLMatrixRow[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setErr(null);
    try {
      const snap = await getLoansPolicy();
      setThresholds(snap.thresholds);
      setMatrix(snap.ecl_matrix);
    } catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); }, []);

  async function saveThresholds() {
    if (!thresholds) return;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await updateLoansPolicyThresholds(thresholds);
      setMsg('Thresholds saved. Next dpd-classifier run will use them.');
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function saveMatrix() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      await updateLoansPolicyMatrix(matrix);
      setMsg('Rate matrix saved. Next provisioning run will use the new rates.');
      await load(); // pick up new effective_from dates
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  function updateThresholdField(field: keyof LoansPolicyThresholds, value: string) {
    if (!thresholds) return;
    const n = parseInt(value, 10);
    if (Number.isNaN(n)) return;
    setThresholds({ ...thresholds, [field]: n });
  }

  function updateMatrixRate(idx: number, value: string) {
    const next = [...matrix];
    next[idx] = { ...next[idx], ecl_rate_pct: value };
    setMatrix(next);
  }

  // Group matrix by SASRA class for the table layout.
  const matrixByClass = useMemo(() => {
    const m: Record<string, ECLMatrixRow[]> = {};
    for (const r of matrix) {
      (m[r.classification_sasra] ||= []).push(r);
    }
    return m;
  }, [matrix]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">Settings · Loans policy</div>
          <h1>DPD thresholds & ECL rate matrix</h1>
          <div className="page-sub">
            These two settings drive every daily DPD classification and every monthly
            provisioning run. Changes take effect on the next dpd-classifier and
            provisioning run respectively. Rate-matrix history is preserved — saving a
            different rate inserts a new effective_from row rather than overwriting.
          </div>
        </div>
      </div>

      {!canWrite && (
        <div className="alert alert-warn" style={{ marginTop: 12 }}>
          You can view but not edit. Editing requires <code>loans:policy:write</code> —
          held by SACCO Admin and Tenant Owner.
        </div>
      )}

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {msg && <div className="alert alert-info" style={{ marginTop: 12 }}>{msg}</div>}

      {/* ─────────── DPD thresholds ─────────── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>DPD thresholds (days past due)</h3>
          <span className="card-sub">Defaults from CBK Prudential 0419</span>
        </div>
        <div className="card-body">
          {!thresholds ? <div className="muted">Loading…</div> : (
            <>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
                <fieldset style={{ border: '1px solid var(--border)', borderRadius: 4, padding: 12 }}>
                  <legend style={{ padding: '0 6px', fontWeight: 600 }}>SASRA</legend>
                  <ThresholdInput label="Watch ≥"        value={thresholds.sasra_watch_dpd}      disabled={!canWrite} onChange={(v) => updateThresholdField('sasra_watch_dpd', v)} hint="First day after due. Default 1." />
                  <ThresholdInput label="Substandard ≥"  value={thresholds.dpd_substandard_days} disabled={!canWrite} onChange={(v) => updateThresholdField('dpd_substandard_days', v)} hint="Default 31." />
                  <ThresholdInput label="Doubtful ≥"     value={thresholds.dpd_doubtful_days}    disabled={!canWrite} onChange={(v) => updateThresholdField('dpd_doubtful_days', v)} hint="Default 91." />
                  <ThresholdInput label="Loss ≥"         value={thresholds.dpd_loss_days}        disabled={!canWrite} onChange={(v) => updateThresholdField('dpd_loss_days', v)} hint="Default 181." />
                </fieldset>
                <fieldset style={{ border: '1px solid var(--border)', borderRadius: 4, padding: 12 }}>
                  <legend style={{ padding: '0 6px', fontWeight: 600 }}>IFRS 9</legend>
                  <ThresholdInput label="Stage 2 ≥"      value={thresholds.ifrs9_stage2_dpd}     disabled={!canWrite} onChange={(v) => updateThresholdField('ifrs9_stage2_dpd', v)} hint="Significant increase in credit risk (SICR). Default 31." />
                  <ThresholdInput label="Stage 3 ≥"      value={thresholds.ifrs9_stage3_dpd}     disabled={!canWrite} onChange={(v) => updateThresholdField('ifrs9_stage3_dpd', v)} hint="Credit-impaired. Default 91." />
                  <p className="muted tiny" style={{ marginTop: 16 }}>
                    SASRA and IFRS 9 use independent thresholds — different regulators,
                    different intent. The dpd-classifier worker writes both per loan per day.
                  </p>
                </fieldset>
              </div>
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 12 }}>
                <button className="btn btn-primary" disabled={busy || !canWrite} onClick={() => void saveThresholds()}>
                  {busy ? 'Saving…' : 'Save thresholds'}
                </button>
              </div>
            </>
          )}
        </div>
      </div>

      {/* ─────────── ECL rate matrix ─────────── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>ECL rate matrix</h3>
          <span className="card-sub">Per (SASRA class, IFRS 9 stage) pair</span>
        </div>
        <div className="card-body">
          {matrix.length === 0 ? <div className="muted">Loading…</div> : (
            <>
              <table className="tbl">
                <thead>
                  <tr>
                    <th>SASRA class</th>
                    <th>IFRS 9 stage</th>
                    <th className="num">ECL rate</th>
                    <th>Effective from</th>
                    <th>Notes</th>
                  </tr>
                </thead>
                <tbody>
                  {SASRA_CLASSES.flatMap((c) =>
                    (matrixByClass[c] ?? []).map((row) => {
                      const idx = matrix.indexOf(row);
                      return (
                        <tr key={`${row.classification_sasra}-${row.classification_ifrs9_stage}`}>
                          <td>{SASRA_LABEL[row.classification_sasra]}</td>
                          <td className="mono">Stage {row.classification_ifrs9_stage}</td>
                          <td className="num">
                            <input
                              className="input"
                              style={{ width: 100, textAlign: 'right' }}
                              type="number"
                              step="0.0001"
                              min="0"
                              max="1"
                              value={row.ecl_rate_pct}
                              disabled={!canWrite}
                              onChange={(e) => updateMatrixRate(idx, e.target.value)}
                            />
                            <span className="muted tiny" style={{ marginLeft: 4 }}>
                              ({(parseFloat(row.ecl_rate_pct) * 100).toFixed(2)}%)
                            </span>
                          </td>
                          <td className="mono muted tiny">{row.effective_from}</td>
                          <td className="muted tiny">{row.notes ?? '—'}</td>
                        </tr>
                      );
                    }),
                  )}
                </tbody>
              </table>
              <p className="muted tiny" style={{ marginTop: 12 }}>
                Values are decimals (1.00 = 100%). Saving inserts a new
                effective_from=today row only for rates that changed; existing rows
                remain so the audit trail of past values is preserved.
              </p>
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                <button className="btn btn-primary" disabled={busy || !canWrite} onClick={() => void saveMatrix()}>
                  {busy ? 'Saving…' : 'Save matrix'}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function ThresholdInput({ label, value, disabled, onChange, hint }: {
  label: string; value: number; disabled?: boolean; onChange: (v: string) => void; hint?: string;
}) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ display: 'flex', justifyContent: 'space-between' }}>
        <span>{label}</span>
        {hint && <span style={{ fontStyle: 'italic' }}>{hint}</span>}
      </div>
      <input
        className="input"
        type="number"
        min={0}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        style={{ width: 110 }}
      />
      <span className="muted tiny" style={{ marginLeft: 6 }}>days</span>
    </label>
  );
}
