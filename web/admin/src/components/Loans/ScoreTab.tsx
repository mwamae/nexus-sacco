// Phase-1 follow-up — Score tab replacement.
//
// Drops the raw-JSON dump that used to live in LoanApplicationDetail.
// Renders:
//   - Header card (score + risk + verdict pill + Re-score button)
//   - Affordability waterfall
//   - Multiplier check
//   - Hard blocks + advisories (parsed from scoring_flags JSON)
//   - Per-factor breakdown (parsed from scoring_details JSON)
//   - Score history timeline (loaded from /score/history)
//
// Renders cleanly for empty / partial data.

import { useEffect, useMemo, useState } from 'react';
import {
  rescoreApplication,
  getScoreHistory,
  type LoanApplication,
  type LoanScoreHistoryEntry,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = {
  application: LoanApplication;
  onRescored?: () => void;
};

type Flag = {
  code: string;
  severity: 'hard_block' | 'soft_flag' | 'advisory';
  message: string;
  action?: string;
};

type Factor = {
  code?: string;
  label?: string;
  name?: string;
  weight_pct?: number;
  weight?: number;
  score?: number | null;
  contribution?: number;
  note?: string;
};

function fmtKES(s?: string | null): string {
  if (!s) return '—';
  const n = parseFloat(s);
  if (Number.isNaN(n)) return s;
  return n.toLocaleString('en-KE', { maximumFractionDigits: 2 });
}

function ageLabel(ts: string): string {
  const diff = Date.now() - new Date(ts).getTime();
  const min = Math.floor(diff / 60_000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min}m ago`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function parseScoreFlags(raw: unknown): Flag[] {
  if (!raw) return [];
  if (Array.isArray(raw)) return raw as Flag[];
  if (typeof raw === 'string') { try { return JSON.parse(raw) as Flag[]; } catch { return []; } }
  return [];
}

function parseScoreFactors(raw: unknown): Factor[] {
  if (!raw) return [];
  if (Array.isArray(raw)) return raw as Factor[];
  if (typeof raw === 'object') {
    const r = raw as { factors?: Factor[] };
    if (r.factors) return r.factors;
  }
  if (typeof raw === 'string') { try { const obj = JSON.parse(raw); return Array.isArray(obj) ? obj : (obj.factors ?? []); } catch { return []; } }
  return [];
}

function riskColor(band?: string | null): string {
  switch (band) {
    case 'A': case 'green':  return 'var(--success-fg, #146c43)';
    case 'B': case 'amber':  return 'var(--warning-fg, #8a6d00)';
    case 'C':                return 'var(--warning-fg, #8a6d00)';
    case 'D': case 'red':    return 'var(--danger-fg, #b42318)';
    default:                 return 'var(--muted, #888)';
  }
}

export default function ScoreTab({ application, onRescored }: Props) {
  const { hasPermission } = useAuth();
  const a = application;
  const [history, setHistory] = useState<LoanScoreHistoryEntry[]>([]);
  const [rescoring, setRescoring] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function loadHistory() {
    try { setHistory((await getScoreHistory(a.id)).items); }
    catch { setHistory([]); }
  }
  useEffect(() => { void loadHistory(); }, [a.id]);

  async function doRescore() {
    if (!window.confirm('Re-score this application now? Recomputes the score using the latest data and creates a new history entry.')) return;
    setRescoring(true); setErr(null);
    try { await rescoreApplication(a.id, 'manual_rescore'); await loadHistory(); onRescored?.(); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Re-score failed.'); }
    finally { setRescoring(false); }
  }

  const flags = useMemo(() => parseScoreFlags(a.scoring_flags), [a.scoring_flags]);
  const factors = useMemo(() => parseScoreFactors(a.scoring_details), [a.scoring_details]);
  const hardBlocks = flags.filter((f) => f.severity === 'hard_block');
  const advisories = flags.filter((f) => f.severity !== 'hard_block');

  // Empty-state — no scoring data yet.
  if (a.credit_score == null && !a.scored_at && history.length === 0) {
    return (
      <div className="card" style={{ padding: 16 }}>
        <h3 style={{ marginTop: 0 }}>Not yet scored</h3>
        <div className="muted" style={{ marginBottom: 10 }}>
          The application doesn&rsquo;t have a score yet. Capturing income, employment, and at
          least one guarantor before re-scoring tends to produce a usable verdict.
        </div>
        <button className="btn btn-primary" disabled={rescoring || !hasPermission('loans:apply')} onClick={() => void doRescore()}>
          {rescoring ? 'Scoring…' : 'Re-score now'}
        </button>
        {err && <div className="alert alert-error" style={{ marginTop: 10 }}>{err}</div>}
      </div>
    );
  }

  const passVerdict = a.affordability_pass !== false && hardBlocks.length === 0;
  const verdictBg = passVerdict ? 'var(--success-bg, #e7f4ec)' : 'var(--danger-bg, #fdecea)';
  const verdictFg = passVerdict ? 'var(--success-fg, #146c43)' : 'var(--danger-fg, #b42318)';

  return (
    <div>
      {/* Header card */}
      <div className="card" style={{ padding: 14, marginBottom: 12 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 18, flexWrap: 'wrap' }}>
          <div>
            <div className="muted tiny">Score</div>
            <div style={{ fontSize: 32, fontWeight: 700, color: riskColor(a.risk_band) }}>
              {a.credit_score ?? '—'}
            </div>
          </div>
          <div>
            <div className="muted tiny">Risk band</div>
            <div style={{ fontWeight: 600, color: riskColor(a.risk_band) }}>{a.risk_band ?? '—'}</div>
          </div>
          <div>
            <div className="muted tiny">Verdict</div>
            <span style={{ background: verdictBg, color: verdictFg, padding: '4px 10px', borderRadius: 14, fontWeight: 600 }}>
              {passVerdict ? '✓ APPROVE recommended' : '✗ DECLINE recommended'}
            </span>
          </div>
          <div style={{ flex: 1 }} />
          <button className="btn btn-primary" disabled={rescoring || !hasPermission('loans:apply')} onClick={() => void doRescore()}>
            {rescoring ? 'Scoring…' : 'Re-score'}
          </button>
        </div>
        {a.scored_at && (
          <div className="muted tiny" style={{ marginTop: 8 }}>
            Last scored {ageLabel(a.scored_at)}
            {history[0]?.trigger_reason && ` (${history[0].trigger_reason})`}
          </div>
        )}
        {err && <div className="alert alert-error" style={{ marginTop: 10 }}>{err}</div>}
      </div>

      {/* Affordability waterfall */}
      <div className="card" style={{ padding: 14, marginBottom: 12 }}>
        <h3 style={{ marginTop: 0 }}>Affordability</h3>
        <table className="tbl">
          <tbody>
            <tr><td>Monthly net income</td>             <td className="num mono">{fmtKES(a.monthly_net_income)}</td></tr>
            {a.other_income && parseFloat(a.other_income) > 0 && (
              <tr><td>Other income</td>                 <td className="num mono">{fmtKES(a.other_income)}</td></tr>
            )}
            <tr><td className="muted">Less: monthly expenses</td> <td className="num mono">– {fmtKES(a.monthly_expenses)}</td></tr>
            <tr><td className="muted">Less: existing obligations</td><td className="num mono">– {fmtKES(a.monthly_existing_obligations)}</td></tr>
            <tr style={{ fontWeight: 600 }}>
              <td>Net disposable income</td>
              <td className="num mono">{fmtKES(a.net_disposable_income)}</td>
            </tr>
            <tr>
              <td>DTI ratio</td>
              <td className="num mono">{a.dti_ratio ? `${(parseFloat(a.dti_ratio) * 100).toFixed(1)}%` : '—'}</td>
            </tr>
            <tr>
              <td>Affordability test</td>
              <td className="num" style={{ color: a.affordability_pass ? 'var(--success-fg, #146c43)' : 'var(--danger-fg, #b42318)', fontWeight: 600 }}>
                {a.affordability_pass ? '✓ pass' : '✗ fail'}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      {/* Multiplier check */}
      <div className="card" style={{ padding: 14, marginBottom: 12 }}>
        <h3 style={{ marginTop: 0 }}>Multiplier check</h3>
        <table className="tbl">
          <tbody>
            <tr><td>Ceiling (computed max amount)</td> <td className="num mono">{fmtKES(a.computed_max_amount)}</td></tr>
            <tr><td>Requested</td>                     <td className="num mono">{fmtKES(a.requested_amount)}</td></tr>
            <tr>
              <td>Headroom</td>
              <td className="num mono">
                {a.computed_max_amount && a.requested_amount ?
                  ((): React.ReactNode => {
                    const diff = parseFloat(a.computed_max_amount) - parseFloat(a.requested_amount);
                    return <span style={{ color: diff < 0 ? 'var(--danger-fg, #b42318)' : undefined }}>
                      {diff < 0 ? '– ' : ''}{fmtKES(Math.abs(diff).toString())}
                    </span>;
                  })()
                  : '—'}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      {/* Hard blocks */}
      {hardBlocks.length > 0 && (
        <div className="card" style={{ padding: 14, marginBottom: 12, borderLeft: '4px solid var(--danger-fg, #b42318)' }}>
          <h3 style={{ marginTop: 0, color: 'var(--danger-fg, #b42318)' }}>Hard blocks</h3>
          {hardBlocks.map((f, i) => (
            <div key={i} style={{ padding: '6px 0', borderBottom: '1px solid var(--border, #eee)' }}>
              <div className="mono tiny">{f.code}</div>
              <div>{f.message}</div>
              {f.action && <div className="muted tiny">Action: {f.action}</div>}
            </div>
          ))}
        </div>
      )}

      {/* Advisories */}
      {advisories.length > 0 && (
        <div className="card" style={{ padding: 14, marginBottom: 12, borderLeft: '4px solid var(--warning-fg, #8a6d00)' }}>
          <h3 style={{ marginTop: 0, color: 'var(--warning-fg, #8a6d00)' }}>Advisories</h3>
          {advisories.map((f, i) => (
            <div key={i} style={{ padding: '6px 0', borderBottom: '1px solid var(--border, #eee)' }}>
              <div className="mono tiny">{f.code} · {f.severity}</div>
              <div>{f.message}</div>
              {f.action && <div className="muted tiny">Action: {f.action}</div>}
            </div>
          ))}
        </div>
      )}

      {/* Per-factor breakdown */}
      {factors.length > 0 && (
        <div className="card" style={{ padding: 14, marginBottom: 12 }}>
          <h3 style={{ marginTop: 0 }}>Factor breakdown</h3>
          <table className="tbl">
            <thead><tr><th>Factor</th><th className="num">Weight</th><th className="num">Score</th><th className="num">Contribution</th></tr></thead>
            <tbody>
              {factors.map((f, i) => (
                <tr key={i}>
                  <td>{f.label ?? f.name ?? f.code ?? '—'}</td>
                  <td className="num">{(f.weight_pct ?? f.weight ?? 0)}%</td>
                  <td className="num mono">{f.score == null ? '—' : f.score}</td>
                  <td className="num mono">{f.contribution == null ? (f.note ?? '—') : f.contribution}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* History timeline */}
      <div className="card" style={{ padding: 14 }}>
        <h3 style={{ marginTop: 0 }}>Score history</h3>
        {history.length === 0 ? (
          <div className="muted">No history yet.</div>
        ) : (
          <ol style={{ paddingLeft: 18, margin: 0 }}>
            {history.map((h) => (
              <li key={h.id} style={{ marginBottom: 10 }}>
                <div style={{ fontWeight: 600 }}>
                  {h.credit_score ?? '—'} · {h.risk_band ?? '—'}
                  <span className="muted tiny" style={{ marginLeft: 8 }}>{h.trigger_reason ?? ''}</span>
                </div>
                <div className="muted tiny">{ageLabel(h.scored_at)} · {new Date(h.scored_at).toLocaleString()}</div>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}
