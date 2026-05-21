// Bank account detail — upload statement, browse statements, view
// statement lines with match controls, run reconciliation.

import { useEffect, useMemo, useState } from 'react';
import {
  bankReconciliation,
  excludeBankLine,
  getBankAccount,
  getBankStatement,
  listBankStatements,
  matchBankLine,
  postBankAdjustment,
  suggestMatchesForLine,
  unmatchBankLine,
  uploadBankStatement,
  type BankAccount,
  type BankStatement,
  type BankStatementLine,
  type MatchCandidate,
  type ReconciliationReport,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function BankAccountDetailPage() {
  const { tenant } = useAuth();
  const id = window.location.pathname.split('/').pop() ?? '';
  const today = new Date().toISOString().slice(0, 10);

  const [acct, setAcct] = useState<BankAccount | null>(null);
  const [statements, setStatements] = useState<BankStatement[]>([]);
  const [selectedStmt, setSelectedStmt] = useState<string | null>(null);
  const [lines, setLines] = useState<BankStatementLine[]>([]);
  const [recon, setRecon] = useState<ReconciliationReport | null>(null);
  const [asOf, setAsOf] = useState(today);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // line-level match dialog
  const [activeLine, setActiveLine] = useState<BankStatementLine | null>(null);
  const [candidates, setCandidates] = useState<MatchCandidate[]>([]);
  const [tolerance, setTolerance] = useState(5);
  // adjustment dialog
  const [adjLine, setAdjLine] = useState<BankStatementLine | null>(null);
  const [adjAccount, setAdjAccount] = useState('5400');
  const [adjNarration, setAdjNarration] = useState('');

  async function loadAll() {
    setErr(null);
    try {
      const [a, s, r] = await Promise.all([
        getBankAccount(id),
        listBankStatements(id),
        bankReconciliation(id, asOf),
      ]);
      setAcct(a); setStatements(s.items); setRecon(r);
      if (s.items.length > 0 && !selectedStmt) setSelectedStmt(s.items[0].id);
    } catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void loadAll(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  useEffect(() => {
    if (!selectedStmt) { setLines([]); return; }
    void (async () => {
      try { setLines((await getBankStatement(selectedStmt)).lines); }
      catch (e) { setErr(asMsg(e)); }
    })();
  }, [selectedStmt]);

  async function reloadRecon() {
    try { setRecon(await bankReconciliation(id, asOf)); }
    catch (e) { setErr(asMsg(e)); }
  }

  async function upload(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setBusy(true); setErr(null);
    try {
      const stmt = await uploadBankStatement(id, f);
      setSelectedStmt(stmt.id);
      await loadAll();
    } catch (er) { setErr(asMsg(er)); }
    finally { setBusy(false); e.target.value = ''; }
  }

  async function openMatch(line: BankStatementLine) {
    setActiveLine(line);
    try {
      const r = await suggestMatchesForLine(line.id, tolerance);
      setCandidates(r.items);
    } catch (e) { setErr(asMsg(e)); }
  }

  async function doMatch(c: MatchCandidate) {
    if (!activeLine) return;
    setBusy(true); setErr(null);
    try {
      await matchBankLine(activeLine.id, c.journal_line_id);
      setActiveLine(null); setCandidates([]);
      if (selectedStmt) setLines((await getBankStatement(selectedStmt)).lines);
      await reloadRecon();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doUnmatch(line: BankStatementLine) {
    setBusy(true); setErr(null);
    try {
      await unmatchBankLine(line.id);
      if (selectedStmt) setLines((await getBankStatement(selectedStmt)).lines);
      await reloadRecon();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doExclude(line: BankStatementLine) {
    const reason = prompt('Reason for excluding this line?', 'Internal transfer — recorded elsewhere');
    if (!reason) return;
    setBusy(true); setErr(null);
    try {
      await excludeBankLine(line.id, reason);
      if (selectedStmt) setLines((await getBankStatement(selectedStmt)).lines);
      await reloadRecon();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doAdjust() {
    if (!adjLine) return;
    setBusy(true); setErr(null);
    try {
      await postBankAdjustment(adjLine.id, {
        offset_account_code: adjAccount,
        narration: adjNarration || undefined,
      });
      setAdjLine(null); setAdjNarration('');
      if (selectedStmt) setLines((await getBankStatement(selectedStmt)).lines);
      await reloadRecon();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const statusColors: Record<string, string> = useMemo(() => ({
    unmatched: 'var(--muted)',
    matched: 'var(--pos)',
    manual_match: '#3b6ab8',
    excluded: '#888',
    adjusted: '#c97a00',
  }), []);

  if (!acct) {
    return <div className="page"><div className="muted">Loading…</div></div>;
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Bank reconciliation</div>
          <h1>{acct.bank_name} · {acct.account_number}</h1>
          <div className="page-sub">
            GL account <span className="mono">{acct.gl_account_code}</span> · {acct.currency_code} · {acct.branch ?? 'no branch'}
          </div>
        </div>
        <a className="btn" href="/bank-accounts">← All accounts</a>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {/* ─── Reconciliation summary ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Reconciliation</h3>
          <span className="card-sub">As of <input type="date" value={asOf} onChange={(e) => { setAsOf(e.target.value); }} style={{ marginLeft: 6 }} /></span>
        </div>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 18 }}>
          <Stat label="GL balance" value={recon?.gl_balance ?? '—'} mono />
          <Stat label="Statement balance" value={recon?.statement_balance ?? '—'} mono />
          <Stat
            label="Variance"
            value={recon?.variance ?? '—'}
            mono
            color={recon ? (recon.reconciled ? 'var(--pos)' : 'var(--neg)') : undefined}
          />
          <Stat
            label="Reconciled?"
            value={recon ? (recon.reconciled ? '✓ Yes' : '✗ No') : '—'}
            color={recon ? (recon.reconciled ? 'var(--pos)' : 'var(--neg)') : undefined}
          />
          <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 12, justifyContent: 'space-between', flexWrap: 'wrap', borderTop: '1px solid var(--border)', paddingTop: 12 }}>
            <div className="tiny muted">
              Outstanding bank: <span className="mono">+{recon?.outstanding_bank_credit ?? '0'} / -{recon?.outstanding_bank_debit ?? '0'}</span>
              {' · '}
              Outstanding GL: <span className="mono">+{recon?.outstanding_gl_debit ?? '0'} / -{recon?.outstanding_gl_credit ?? '0'}</span>
            </div>
            <button className="btn" onClick={() => void reloadRecon()}>Refresh</button>
          </div>
        </div>
      </div>

      {/* ─── Upload statement ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Upload statement</h3></div>
        <div className="card-body">
          <div className="muted tiny" style={{ marginBottom: 8 }}>
            CSV with headers <code>txn_date,value_date,description,reference,debit,credit,running_balance</code>.
            Dates accept YYYY-MM-DD or DD/MM/YYYY.
          </div>
          <input type="file" accept=".csv,text/csv" onChange={upload} disabled={busy} />
        </div>
      </div>

      {/* ─── Statements + lines ─── */}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
        <div className="card">
          <div className="card-hd">
            <h3>Statements</h3>
            <span className="card-sub">{statements.length}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Date</th><th className="num">Lines</th><th className="num">Closing</th></tr></thead>
              <tbody>
                {statements.map((s) => (
                  <tr key={s.id} onClick={() => setSelectedStmt(s.id)} style={{
                    cursor: 'pointer',
                    background: selectedStmt === s.id ? 'var(--surface-2)' : undefined,
                  }}>
                    <td className="mono">{s.statement_date.slice(0, 10)}</td>
                    <td className="num mono">{s.line_count}</td>
                    <td className="num mono">{s.closing_balance ?? '—'}</td>
                  </tr>
                ))}
                {statements.length === 0 && (
                  <tr><td colSpan={3} className="muted" style={{ textAlign: 'center', padding: 14 }}>No statements uploaded.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-hd">
            <h3>Statement lines</h3>
            <span className="card-sub">{lines.length}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Date</th>
                  <th>Description</th>
                  <th className="num">Debit</th>
                  <th className="num">Credit</th>
                  <th>Status</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {lines.map((l) => (
                  <tr key={l.id}>
                    <td className="mono tiny">{l.txn_date.slice(0, 10)}</td>
                    <td>
                      <div>{l.description ?? <span className="muted">—</span>}</div>
                      {l.reference && <div className="muted tiny mono">{l.reference}</div>}
                    </td>
                    <td className="num mono">{nonZero(l.debit)}</td>
                    <td className="num mono">{nonZero(l.credit)}</td>
                    <td>
                      <span style={{ color: statusColors[l.match_status], fontWeight: 600 }}>
                        {l.match_status}
                      </span>
                    </td>
                    <td style={{ whiteSpace: 'nowrap' }}>
                      {l.match_status === 'unmatched' ? (
                        <>
                          <button className="btn" style={{ fontSize: 12 }} onClick={() => void openMatch(l)}>Match</button>{' '}
                          <button className="btn" style={{ fontSize: 12 }} onClick={() => setAdjLine(l)}>Adjust</button>{' '}
                          <button className="btn" style={{ fontSize: 12 }} onClick={() => void doExclude(l)}>Exclude</button>
                        </>
                      ) : (l.match_status === 'matched' || l.match_status === 'manual_match' || l.match_status === 'adjusted') ? (
                        <button className="btn" style={{ fontSize: 12 }} onClick={() => void doUnmatch(l)}>Unmatch</button>
                      ) : null}
                    </td>
                  </tr>
                ))}
                {lines.length === 0 && (
                  <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 14 }}>
                    {selectedStmt ? 'No lines on this statement.' : 'Pick a statement.'}
                  </td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {/* ─── Match dialog ─── */}
      {activeLine && (
        <Dialog title={`Match line — ${activeLine.txn_date.slice(0, 10)} · ${activeLine.description ?? ''}`}
                onClose={() => { setActiveLine(null); setCandidates([]); }}>
          <div style={{ display: 'flex', gap: 12, marginBottom: 8 }}>
            <div>
              Bank amount: <strong className="mono">{!activeLine.credit.startsWith('0') ? '+' + activeLine.credit : '-' + activeLine.debit}</strong>
            </div>
            <label>
              <span className="muted tiny">Day tolerance:</span>{' '}
              <input type="number" min={0} max={30} value={tolerance}
                     onChange={(e) => setTolerance(parseInt(e.target.value || '5', 10))}
                     style={{ width: 60 }} />
              <button className="btn tiny" onClick={() => void openMatch(activeLine)}>Re-search</button>
            </label>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Date</th><th>Entry</th><th>Narration</th><th className="num">Debit</th><th className="num">Credit</th><th></th></tr></thead>
              <tbody>
                {candidates.map((c) => (
                  <tr key={c.journal_line_id}>
                    <td className="mono tiny">{c.entry_date.slice(0, 10)}</td>
                    <td className="mono">{c.entry_no}</td>
                    <td>{c.narration}{c.source_module && <span className="muted tiny"> ({c.source_module})</span>}</td>
                    <td className="num mono">{nonZero(c.debit)}</td>
                    <td className="num mono">{nonZero(c.credit)}</td>
                    <td><button className="btn btn-primary" onClick={() => void doMatch(c)} disabled={busy}>Pick</button></td>
                  </tr>
                ))}
                {candidates.length === 0 && (
                  <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 14 }}>
                    No candidate GL lines found. Try a wider tolerance or post an adjustment.
                  </td></tr>
                )}
              </tbody>
            </table>
          </div>
        </Dialog>
      )}

      {/* ─── Adjustment dialog ─── */}
      {adjLine && (
        <Dialog title={`Adjustment — ${adjLine.txn_date.slice(0, 10)} · ${adjLine.description ?? ''}`}
                onClose={() => setAdjLine(null)}>
          <div className="card-body" style={{ display: 'grid', gap: 12 }}>
            <div>
              Bank amount: <strong className="mono">{!adjLine.credit.startsWith('0') ? '+' + adjLine.credit : '-' + adjLine.debit}</strong> on <span className="mono">{acct.gl_account_code}</span>
            </div>
            <label>
              <div className="muted tiny">Offset account code (the other leg)</div>
              <input value={adjAccount} onChange={(e) => setAdjAccount(e.target.value)} placeholder="5400" />
              <div className="muted tiny" style={{ marginTop: 2 }}>
                Typical offsets: 5400 Bank charges, 4100 Investment income, 5300 Rent and Utilities.
              </div>
            </label>
            <label>
              <div className="muted tiny">Narration</div>
              <input value={adjNarration} onChange={(e) => setAdjNarration(e.target.value)} placeholder="Bank charge for May statement" />
            </label>
            <div style={{ textAlign: 'right' }}>
              <button className="btn btn-primary" disabled={busy || !adjAccount} onClick={() => void doAdjust()}>
                {busy ? 'Posting…' : 'Post adjustment'}
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function Stat({ label, value, mono, color }: { label: string; value: string; mono?: boolean; color?: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{ fontSize: 18, fontWeight: 700, fontFamily: mono ? 'var(--font-mono)' : undefined, color }}>
        {value}
      </div>
    </div>
  );
}

function Dialog({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 99,
    }} onClick={onClose}>
      <div className="card" style={{ minWidth: 600, maxWidth: '90vw', maxHeight: '80vh', overflowY: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd"><h3>{title}</h3><button className="btn tiny" onClick={onClose}>Close</button></div>
        {children}
      </div>
    </div>
  );
}

function nonZero(v: string): string {
  return v === '0' || v === '0.00' ? '' : v;
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
