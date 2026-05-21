// Cash & Float Management — single page with:
//   1. Cash position overview (vault + tills + grand total)
//   2. Tills list with current session indicator
//   3. Selected till panel: current session + open/close + transfer
//
// Vault↔till and till↔till transfers post to the GL automatically;
// session open + close post opening float and any variance.

import { useEffect, useMemo, useState } from 'react';
import {
  closeTillSession,
  createCashTransfer,
  createTill,
  getCashPosition,
  getTillDetail,
  listCashTransfers,
  listTillSessions,
  listTills,
  openTillSession,
  type CashPosition,
  type CashTransfer,
  type Till,
  type TillSession,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function CashManagementPage() {
  const { tenant, user } = useAuth();
  const [position, setPosition] = useState<CashPosition | null>(null);
  const [tills, setTills] = useState<Till[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ till: Till; current_session?: TillSession | null } | null>(null);
  const [sessions, setSessions] = useState<TillSession[]>([]);
  const [transfers, setTransfers] = useState<CashTransfer[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // New-till form
  const [newTillCode, setNewTillCode] = useState('');
  const [newTillName, setNewTillName] = useState('');
  const [newTillBranch, setNewTillBranch] = useState('');
  const [showNewTill, setShowNewTill] = useState(false);

  // Open-session form
  const [openFloat, setOpenFloat] = useState('');

  // Close-session form
  const [actualClose, setActualClose] = useState('');
  const [closeNotes, setCloseNotes] = useState('');

  // Transfer form
  const [trType, setTrType] = useState<'vault_to_till' | 'till_to_vault' | 'till_to_till'>('vault_to_till');
  const [trAmount, setTrAmount] = useState('');
  const [trOtherTill, setTrOtherTill] = useState('');
  const [trReference, setTrReference] = useState('');

  async function loadAll() {
    setErr(null);
    try {
      const [p, t, tr] = await Promise.all([getCashPosition(), listTills(), listCashTransfers()]);
      setPosition(p); setTills(t.items); setTransfers(tr.items);
      if (!selected && t.items.length > 0) setSelected(t.items[0].id);
    } catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void loadAll(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  useEffect(() => {
    if (!selected) { setDetail(null); setSessions([]); return; }
    void (async () => {
      try {
        const [d, s] = await Promise.all([getTillDetail(selected), listTillSessions(selected)]);
        setDetail(d); setSessions(s.items);
      } catch (e) { setErr(asMsg(e)); }
    })();
  }, [selected]);

  async function reloadDetail() {
    if (!selected) return;
    const [d, s] = await Promise.all([getTillDetail(selected), listTillSessions(selected)]);
    setDetail(d); setSessions(s.items);
  }

  async function doCreateTill() {
    setBusy(true); setErr(null);
    try {
      const t = await createTill({
        code: newTillCode, name: newTillName,
        branch: newTillBranch || undefined,
      });
      setNewTillCode(''); setNewTillName(''); setNewTillBranch(''); setShowNewTill(false);
      await loadAll();
      setSelected(t.id);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doOpenSession() {
    if (!detail) return;
    setBusy(true); setErr(null);
    try {
      await openTillSession({
        till_id: detail.till.id,
        teller_user_id: user?.id ?? '',
        opening_float: openFloat,
      });
      setOpenFloat('');
      await Promise.all([loadAll(), reloadDetail()]);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doCloseSession() {
    if (!detail?.current_session) return;
    setBusy(true); setErr(null);
    try {
      await closeTillSession(detail.current_session.id, actualClose, closeNotes || undefined);
      setActualClose(''); setCloseNotes('');
      await Promise.all([loadAll(), reloadDetail()]);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doTransfer() {
    if (!detail) return;
    setBusy(true); setErr(null);
    try {
      let input: Parameters<typeof createCashTransfer>[0];
      if (trType === 'vault_to_till') {
        input = { transfer_type: trType, to_till_id: detail.till.id, amount: trAmount, reference: trReference || undefined };
      } else if (trType === 'till_to_vault') {
        input = { transfer_type: trType, from_till_id: detail.till.id, amount: trAmount, reference: trReference || undefined };
      } else {
        if (!trOtherTill) { setErr('Pick a destination till'); setBusy(false); return; }
        input = { transfer_type: trType, from_till_id: detail.till.id, to_till_id: trOtherTill, amount: trAmount, reference: trReference || undefined };
      }
      await createCashTransfer(input);
      setTrAmount(''); setTrReference(''); setTrOtherTill('');
      await Promise.all([loadAll(), reloadDetail()]);
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const otherTills = useMemo(() => tills.filter((t) => t.id !== selected), [tills, selected]);
  const TYPE_COLORS: Record<string, string> = {
    opening_float: '#3b6ab8',
    vault_to_till: '#3b6ab8',
    till_to_vault: '#0a7a39',
    till_to_till: '#c97a00',
    variance_adjustment: 'var(--neg)',
    closing_return: '#0a7a39',
  };

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Cash management</div>
          <h1>Cash & Float</h1>
          <div className="page-sub">
            Vault and till operations. Opening floats, transfers and variance adjustments post to the GL automatically.
          </div>
        </div>
        <button className="btn btn-primary" onClick={() => setShowNewTill(!showNewTill)}>
          {showNewTill ? 'Cancel' : 'Register till'}
        </button>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {/* ─── Position overview ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Cash position</h3></div>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 18 }}>
          <Stat label="Vault (GL 1000)" value={position?.vault_balance ?? '—'} />
          <Stat label="All tills (GL 1010)" value={position?.till_balance ?? '—'} />
          <Stat
            label="Variance (GL 2250)"
            value={position?.variance_balance ?? '—'}
            color={position && parseFloat(position.variance_balance) !== 0 ? 'var(--neg)' : 'var(--muted)'}
          />
          <Stat label="Grand total cash" value={position?.grand_total ?? '—'} bold />
        </div>
      </div>

      {showNewTill && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd"><h3>New till</h3></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
            <label><div className="muted tiny">Code</div><input value={newTillCode} onChange={(e) => setNewTillCode(e.target.value)} placeholder="T1" /></label>
            <label><div className="muted tiny">Name</div><input value={newTillName} onChange={(e) => setNewTillName(e.target.value)} placeholder="Front desk till 1" /></label>
            <label><div className="muted tiny">Branch</div><input value={newTillBranch} onChange={(e) => setNewTillBranch(e.target.value)} /></label>
            <div style={{ gridColumn: '1 / -1', textAlign: 'right' }}>
              <button className="btn btn-primary" disabled={busy || !newTillCode || !newTillName} onClick={() => void doCreateTill()}>
                Create
              </button>
            </div>
          </div>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(280px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
        {/* ─── Tills list ─── */}
        <div className="card">
          <div className="card-hd">
            <h3>Tills</h3>
            <span className="card-sub">{tills.length}</span>
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead><tr><th>Code</th><th>Name</th><th>Status</th></tr></thead>
              <tbody>
                {tills.map((t) => {
                  const bd = position?.till_breakdown.find((b) => b.till_id === t.id);
                  return (
                    <tr key={t.id} onClick={() => setSelected(t.id)} style={{
                      cursor: 'pointer',
                      background: selected === t.id ? 'var(--surface-2)' : undefined,
                    }}>
                      <td className="mono">{t.code}</td>
                      <td>{t.name}</td>
                      <td>
                        {bd?.has_open_session ? (
                          <span style={{ color: 'var(--pos)', fontWeight: 600 }}>open · {bd.expected_balance}</span>
                        ) : (
                          <span className="muted">closed</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
                {tills.length === 0 && (
                  <tr><td colSpan={3} className="muted" style={{ textAlign: 'center', padding: 14 }}>No tills yet.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        {/* ─── Selected till ─── */}
        <div className="card">
          <div className="card-hd">
            <h3>{detail ? `Till · ${detail.till.code} ${detail.till.name}` : 'Pick a till'}</h3>
            {detail?.current_session && (
              <span className="card-sub" style={{ color: 'var(--pos)' }}>
                open since {new Date(detail.current_session.opened_at).toLocaleString()}
              </span>
            )}
          </div>
          <div className="card-body" style={{ display: 'grid', gap: 12 }}>
            {!detail && <div className="muted">Pick a till from the list.</div>}

            {detail && !detail.current_session && (
              <div>
                <h4 style={{ margin: 0 }}>Open a session</h4>
                <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end', marginTop: 8 }}>
                  <label>
                    <div className="muted tiny">Opening float</div>
                    <input type="text" value={openFloat} onChange={(e) => setOpenFloat(e.target.value)} placeholder="10000" />
                  </label>
                  <button className="btn btn-primary" disabled={busy || !openFloat} onClick={() => void doOpenSession()}>
                    Open session
                  </button>
                </div>
                <div className="muted tiny" style={{ marginTop: 4 }}>
                  Posts: DR {detail.till.gl_account_code} (till) / CR {detail.till.vault_account_code} (vault)
                </div>
              </div>
            )}

            {detail?.current_session && (
              <>
                <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
                  <Stat label="Opening float" value={detail.current_session.opening_float} />
                  <Stat label="Expected close" value={detail.current_session.expected_close} bold />
                </div>

                <div>
                  <h4 style={{ margin: 0 }}>Transfer</h4>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 8, alignItems: 'flex-end', marginTop: 8 }}>
                    <label>
                      <div className="muted tiny">Type</div>
                      <select value={trType} onChange={(e) => setTrType(e.target.value as typeof trType)}>
                        <option value="vault_to_till">Vault → this till</option>
                        <option value="till_to_vault">This till → vault</option>
                        <option value="till_to_till">This till → other till</option>
                      </select>
                    </label>
                    {trType === 'till_to_till' && (
                      <label>
                        <div className="muted tiny">Other till</div>
                        <select value={trOtherTill} onChange={(e) => setTrOtherTill(e.target.value)}>
                          <option value="">—</option>
                          {otherTills.map((t) => <option key={t.id} value={t.id}>{t.code} · {t.name}</option>)}
                        </select>
                      </label>
                    )}
                    <label>
                      <div className="muted tiny">Amount</div>
                      <input value={trAmount} onChange={(e) => setTrAmount(e.target.value)} placeholder="2500" />
                    </label>
                    <label>
                      <div className="muted tiny">Reference</div>
                      <input value={trReference} onChange={(e) => setTrReference(e.target.value)} />
                    </label>
                    <button className="btn btn-primary" disabled={busy || !trAmount} onClick={() => void doTransfer()}>
                      Transfer
                    </button>
                  </div>
                </div>

                <div>
                  <h4 style={{ margin: 0 }}>Close session</h4>
                  <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end', marginTop: 8, flexWrap: 'wrap' }}>
                    <label>
                      <div className="muted tiny">Actual count</div>
                      <input value={actualClose} onChange={(e) => setActualClose(e.target.value)} placeholder={detail.current_session.expected_close} />
                    </label>
                    <label style={{ flex: 1, minWidth: 220 }}>
                      <div className="muted tiny">Notes</div>
                      <input value={closeNotes} onChange={(e) => setCloseNotes(e.target.value)} placeholder="e.g. counted by supervisor" />
                    </label>
                    <button className="btn btn-primary" disabled={busy || !actualClose} onClick={() => void doCloseSession()}>
                      Close session
                    </button>
                  </div>
                  {actualClose && (
                    <div className="muted tiny" style={{ marginTop: 6 }}>
                      Variance = {actualClose} − {detail.current_session.expected_close} = <strong>
                        {(parseFloat(actualClose) - parseFloat(detail.current_session.expected_close)).toFixed(2)}
                      </strong>
                    </div>
                  )}
                </div>
              </>
            )}

            {/* Session history */}
            {sessions.length > 0 && (
              <div>
                <h4 style={{ margin: 0 }}>Session history</h4>
                <table className="tbl" style={{ marginTop: 8 }}>
                  <thead><tr><th>Opened</th><th className="num">Float</th><th className="num">Expected</th><th className="num">Actual</th><th className="num">Variance</th><th>Status</th></tr></thead>
                  <tbody>
                    {sessions.map((s) => (
                      <tr key={s.id}>
                        <td className="mono tiny">{new Date(s.opened_at).toLocaleString()}</td>
                        <td className="num mono">{s.opening_float}</td>
                        <td className="num mono">{s.expected_close}</td>
                        <td className="num mono">{s.actual_close ?? '—'}</td>
                        <td className="num mono" style={{ color: parseFloat(s.variance) !== 0 ? 'var(--neg)' : 'var(--muted)' }}>
                          {s.variance}
                        </td>
                        <td><span style={{ color: s.status === 'open' ? 'var(--pos)' : 'var(--muted)', fontWeight: 600 }}>{s.status}</span></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* ─── Recent transfers ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Recent transfers</h3>
          <span className="card-sub">{transfers.length}</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead><tr><th>When</th><th>Type</th><th className="num">Amount</th><th>Reference</th></tr></thead>
            <tbody>
              {transfers.slice(0, 20).map((t) => (
                <tr key={t.id}>
                  <td className="mono tiny">{new Date(t.transferred_at).toLocaleString()}</td>
                  <td><span style={{ color: TYPE_COLORS[t.transfer_type], fontWeight: 600 }}>{t.transfer_type}</span></td>
                  <td className="num mono">{t.amount}</td>
                  <td>{t.reference ?? <span className="muted">—</span>}</td>
                </tr>
              ))}
              {transfers.length === 0 && (
                <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 14 }}>No transfers yet.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

function Stat({ label, value, bold, color }: { label: string; value: string; bold?: boolean; color?: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{
        fontSize: bold ? 22 : 18, fontWeight: bold ? 800 : 700,
        fontFamily: 'var(--font-mono)', color,
      }}>{value}</div>
    </div>
  );
}

function asMsg(e: unknown): string {
  if (typeof e === 'object' && e && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'request failed';
}
