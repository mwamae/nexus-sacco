// Fixed Assets — single page combining the asset register, depreciation
// runs, and disposal flow. Acquisition auto-posts (DR gross / CR funded
// from), depreciation runs aggregate per-asset amounts and post one GL
// entry, disposal posts an elimination entry with computed gain/loss.

import { useEffect, useMemo, useState } from 'react';
import {
  createDepreciationRun,
  createFixedAsset,
  disposeFixedAsset,
  getDepreciationRun,
  listDepreciationRuns,
  listFixedAssets,
  postDepreciationRun,
  type DepreciationRun,
  type DepreciationRunLine,
  type FixedAsset,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const CATEGORY_GL: Record<string, string> = {
  furniture: '1500',
  equipment: '1510',
  computer: '1520',
  vehicle: '1530',
  building: '1540',
  land: '1550',
  intangible: '1600',
};

export default function FixedAssetsPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [tab, setTab] = useState<'register' | 'runs'>('register');
  const [assets, setAssets] = useState<FixedAsset[]>([]);
  const [runs, setRuns] = useState<DepreciationRun[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);

  // new asset
  const [showNew, setShowNew] = useState(false);
  const [newAsset, setNewAsset] = useState({
    asset_no: '',
    name: '',
    category: 'computer',
    gl_asset_code: '1520',
    purchase_date: today,
    purchase_cost: '',
    salvage_value: '0',
    useful_life_months: '36',
    depreciation_method: 'straight_line' as 'straight_line' | 'none',
    funded_from_code: '1000',
    supplier: '',
  });

  // dispose
  const [disposing, setDisposing] = useState<FixedAsset | null>(null);
  const [proceeds, setProceeds] = useState('');
  const [proceedsAcct, setProceedsAcct] = useState('1000');

  // run trigger
  const [runDate, setRunDate] = useState(today);
  const [runNotes, setRunNotes] = useState('');
  const [selectedRunID, setSelectedRunID] = useState<string | null>(null);
  const [runDetail, setRunDetail] = useState<{ run: DepreciationRun; lines: DepreciationRunLine[] } | null>(null);

  async function loadAll() {
    setErr(null);
    try {
      const [a, r] = await Promise.all([listFixedAssets(), listDepreciationRuns()]);
      setAssets(a.items); setRuns(r.items);
    } catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void loadAll(); }, []);

  useEffect(() => {
    if (!selectedRunID) { setRunDetail(null); return; }
    void (async () => {
      try { setRunDetail(await getDepreciationRun(selectedRunID)); }
      catch (e) { setErr(asMsg(e)); }
    })();
  }, [selectedRunID]);

  async function doCreate() {
    setBusy(true); setErr(null); setInfo(null);
    try {
      const a = await createFixedAsset({
        asset_no: newAsset.asset_no,
        name: newAsset.name,
        category: newAsset.category,
        gl_asset_code: newAsset.gl_asset_code,
        purchase_date: newAsset.purchase_date,
        purchase_cost: newAsset.purchase_cost,
        salvage_value: newAsset.salvage_value,
        useful_life_months: parseInt(newAsset.useful_life_months || '0', 10),
        depreciation_method: newAsset.depreciation_method,
        funded_from_code: newAsset.funded_from_code,
        supplier: newAsset.supplier || undefined,
      });
      setInfo(`Asset ${a.asset_no} registered, acquisition posted.`);
      setShowNew(false);
      setNewAsset({ ...newAsset, asset_no: '', name: '', purchase_cost: '', supplier: '' });
      await loadAll();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doDispose() {
    if (!disposing) return;
    setBusy(true); setErr(null); setInfo(null);
    try {
      const updated = await disposeFixedAsset(disposing.id, {
        proceeds: proceeds || '0',
        proceeds_account: proceedsAcct,
      });
      setInfo(`Asset ${updated.asset_no} disposed. Gain/loss: ${updated.disposal_gain_loss}`);
      setDisposing(null); setProceeds(''); setProceedsAcct('1000');
      await loadAll();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doCreateRun() {
    setBusy(true); setErr(null); setInfo(null);
    try {
      const run = await createDepreciationRun({ as_of_date: runDate, notes: runNotes || undefined });
      setInfo(`Run computed for ${runDate}: ${run.assets_processed} assets, total ${run.total_depreciation}`);
      setRunNotes('');
      setSelectedRunID(run.id);
      await loadAll();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  async function doPostRun(id: string) {
    setBusy(true); setErr(null); setInfo(null);
    try {
      const run = await postDepreciationRun(id);
      setInfo(`Run posted, journal ${run.journal_entry_id?.slice(0, 8) ?? '—'}`);
      await loadAll();
      setRunDetail(await getDepreciationRun(id));
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  const totalCost = useMemo(() => assets.reduce((sum, a) => sum + parseFloat(a.purchase_cost), 0), [assets]);
  const totalAccum = useMemo(() => assets.reduce((sum, a) => sum + parseFloat(a.accumulated_depreciation), 0), [assets]);
  const netBookValue = totalCost - totalAccum;

  const STATUS_COLOR: Record<string, string> = {
    active: 'var(--pos)',
    disposed: 'var(--muted)',
    written_off: 'var(--neg)',
    fully_depreciated: '#3b6ab8',
    pending: 'var(--muted)',
    computed: '#3b6ab8',
    posted: 'var(--pos)',
    superseded: 'var(--muted)',
    failed: 'var(--neg)',
  };

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Fixed assets</div>
          <h1>Fixed assets</h1>
          <div className="page-sub">
            Asset register, depreciation runs and disposal. Acquisitions auto-post to the asset GL account;
            depreciation runs post DR 5200 / CR 1590; disposals net out the book value with gain/loss.
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button className="btn" data-active={tab === 'register' || undefined} onClick={() => setTab('register')}>Register</button>
          <button className="btn" data-active={tab === 'runs' || undefined} onClick={() => setTab('runs')}>Depreciation runs</button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {info && <div className="alert" style={{ marginTop: 12, background: 'var(--pos-bg, #e6f5ea)', borderColor: 'var(--pos)' }}>{info}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 18 }}>
          <Stat label="Total cost" value={totalCost.toFixed(2)} />
          <Stat label="Accumulated depreciation" value={totalAccum.toFixed(2)} color="var(--neg)" />
          <Stat label="Net book value" value={netBookValue.toFixed(2)} bold />
        </div>
      </div>

      {tab === 'register' && (
        <>
          <div style={{ marginTop: 12, display: 'flex', justifyContent: 'flex-end' }}>
            <button className="btn btn-primary" onClick={() => setShowNew(!showNew)}>
              {showNew ? 'Cancel' : 'Register asset'}
            </button>
          </div>

          {showNew && (
            <div className="card" style={{ marginTop: 12 }}>
              <div className="card-hd"><h3>New asset</h3></div>
              <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
                <label><div className="muted tiny">Asset no</div><input value={newAsset.asset_no} onChange={(e) => setNewAsset({ ...newAsset, asset_no: e.target.value })} placeholder="FA-0001" /></label>
                <label><div className="muted tiny">Name</div><input value={newAsset.name} onChange={(e) => setNewAsset({ ...newAsset, name: e.target.value })} placeholder="Dell Latitude 5520" /></label>
                <label>
                  <div className="muted tiny">Category</div>
                  <select value={newAsset.category} onChange={(e) => {
                    const cat = e.target.value;
                    setNewAsset({ ...newAsset, category: cat, gl_asset_code: CATEGORY_GL[cat] ?? newAsset.gl_asset_code });
                  }}>
                    {Object.keys(CATEGORY_GL).map((c) => <option key={c} value={c}>{c}</option>)}
                  </select>
                </label>
                <label><div className="muted tiny">GL asset code</div><input value={newAsset.gl_asset_code} onChange={(e) => setNewAsset({ ...newAsset, gl_asset_code: e.target.value })} /></label>
                <label><div className="muted tiny">Funded from</div><input value={newAsset.funded_from_code} onChange={(e) => setNewAsset({ ...newAsset, funded_from_code: e.target.value })} placeholder="1000" /></label>
                <label><div className="muted tiny">Supplier</div><input value={newAsset.supplier} onChange={(e) => setNewAsset({ ...newAsset, supplier: e.target.value })} /></label>
                <label><div className="muted tiny">Purchase date</div><input type="date" value={newAsset.purchase_date} onChange={(e) => setNewAsset({ ...newAsset, purchase_date: e.target.value })} /></label>
                <label><div className="muted tiny">Purchase cost</div><input value={newAsset.purchase_cost} onChange={(e) => setNewAsset({ ...newAsset, purchase_cost: e.target.value })} placeholder="120000" /></label>
                <label><div className="muted tiny">Salvage value</div><input value={newAsset.salvage_value} onChange={(e) => setNewAsset({ ...newAsset, salvage_value: e.target.value })} /></label>
                <label>
                  <div className="muted tiny">Method</div>
                  <select value={newAsset.depreciation_method} onChange={(e) => setNewAsset({ ...newAsset, depreciation_method: e.target.value as typeof newAsset.depreciation_method })}>
                    <option value="straight_line">Straight-line</option>
                    <option value="none">None (no depreciation)</option>
                  </select>
                </label>
                <label><div className="muted tiny">Useful life (months)</div><input value={newAsset.useful_life_months} onChange={(e) => setNewAsset({ ...newAsset, useful_life_months: e.target.value })} /></label>
                <div style={{ gridColumn: '1 / -1', textAlign: 'right' }}>
                  <button className="btn btn-primary" disabled={busy || !newAsset.asset_no || !newAsset.name || !newAsset.purchase_cost} onClick={() => void doCreate()}>
                    {busy ? 'Saving…' : 'Register & post acquisition'}
                  </button>
                </div>
              </div>
            </div>
          )}

          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Asset register</h3><span className="card-sub">{assets.length}</span></div>
            <div className="card-body flush">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Asset no</th><th>Name</th><th>Category</th>
                    <th className="num">Cost</th><th className="num">Accum dep</th><th className="num">Book value</th>
                    <th>Status</th><th></th>
                  </tr>
                </thead>
                <tbody>
                  {assets.map((a) => {
                    const bv = parseFloat(a.purchase_cost) - parseFloat(a.accumulated_depreciation);
                    return (
                      <tr key={a.id}>
                        <td className="mono">{a.asset_no}</td>
                        <td>{a.name}</td>
                        <td>{a.category}</td>
                        <td className="num mono">{a.purchase_cost}</td>
                        <td className="num mono">{a.accumulated_depreciation}</td>
                        <td className="num mono"><strong>{bv.toFixed(2)}</strong></td>
                        <td><span style={{ color: STATUS_COLOR[a.status], fontWeight: 600 }}>{a.status}</span></td>
                        <td>
                          {(a.status === 'active' || a.status === 'fully_depreciated') && (
                            <button className="btn tiny" onClick={() => setDisposing(a)}>Dispose</button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                  {assets.length === 0 && (
                    <tr><td colSpan={8} className="muted" style={{ textAlign: 'center', padding: 18 }}>No assets registered.</td></tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}

      {tab === 'runs' && (
        <>
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Compute new run</h3></div>
            <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end', flexWrap: 'wrap' }}>
              <label>
                <div className="muted tiny">As of date</div>
                <input type="date" value={runDate} onChange={(e) => setRunDate(e.target.value)} />
              </label>
              <label style={{ flex: 1, minWidth: 240 }}>
                <div className="muted tiny">Notes</div>
                <input value={runNotes} onChange={(e) => setRunNotes(e.target.value)} placeholder="Month-end depreciation May 2026" />
              </label>
              <button className="btn btn-primary" disabled={busy} onClick={() => void doCreateRun()}>
                {busy ? 'Computing…' : 'Compute depreciation'}
              </button>
            </div>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(320px, 1fr) 2fr', gap: 12, marginTop: 12 }}>
            <div className="card">
              <div className="card-hd"><h3>Runs</h3><span className="card-sub">{runs.length}</span></div>
              <div className="card-body flush">
                <table className="tbl">
                  <thead><tr><th>Period</th><th>Status</th><th className="num">Total</th></tr></thead>
                  <tbody>
                    {runs.map((r) => (
                      <tr key={r.id} onClick={() => setSelectedRunID(r.id)} style={{ cursor: 'pointer', background: selectedRunID === r.id ? 'var(--surface-2)' : undefined }}>
                        <td className="mono">{String(r.period_year).padStart(4,'0')}-{String(r.period_month).padStart(2, '0')}</td>
                        <td><span style={{ color: STATUS_COLOR[r.status], fontWeight: 600 }}>{r.status}</span></td>
                        <td className="num mono">{r.total_depreciation}</td>
                      </tr>
                    ))}
                    {runs.length === 0 && (<tr><td colSpan={3} className="muted" style={{ textAlign: 'center', padding: 14 }}>No runs yet.</td></tr>)}
                  </tbody>
                </table>
              </div>
            </div>

            <div className="card">
              <div className="card-hd">
                <h3>{runDetail ? `Run · ${runDetail.run.period_year}-${String(runDetail.run.period_month).padStart(2,'0')}` : 'Detail'}</h3>
                {runDetail && (
                  <span className="card-sub" style={{ color: STATUS_COLOR[runDetail.run.status] }}>{runDetail.run.status}</span>
                )}
              </div>
              <div className="card-body" style={{ display: 'grid', gap: 12 }}>
                {!runDetail && <div className="muted">Pick a run from the list.</div>}
                {runDetail && (
                  <>
                    <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
                      <Stat label="Assets processed" value={String(runDetail.run.assets_processed)} />
                      <Stat label="Total depreciation" value={runDetail.run.total_depreciation} mono bold />
                      {runDetail.run.journal_entry_id && (
                        <Stat label="Journal" value={runDetail.run.journal_entry_id.slice(0, 8) + '…'} mono />
                      )}
                    </div>

                    {runDetail.run.status === 'computed' && (
                      <button className="btn btn-primary" disabled={busy} onClick={() => void doPostRun(runDetail.run.id)}>
                        {busy ? 'Posting…' : 'Post to GL'}
                      </button>
                    )}

                    {runDetail.lines.length > 0 && (
                      <div className="card-body flush">
                        <table className="tbl">
                          <thead>
                            <tr>
                              <th>Asset no</th><th>Name</th>
                              <th className="num">Cost</th>
                              <th className="num">Before</th>
                              <th className="num">Δ</th>
                              <th className="num">After</th>
                              <th className="num">Book value</th>
                              <th className="num">Months</th>
                            </tr>
                          </thead>
                          <tbody>
                            {runDetail.lines.map((ln) => (
                              <tr key={ln.id}>
                                <td className="mono">{ln.asset_no}</td>
                                <td>{ln.asset_name}</td>
                                <td className="num mono">{ln.cost}</td>
                                <td className="num mono">{ln.accumulated_before}</td>
                                <td className="num mono"><strong>{ln.depreciation_amount}</strong></td>
                                <td className="num mono">{ln.accumulated_after}</td>
                                <td className="num mono">{ln.book_value_after}</td>
                                <td className="num">{ln.months_depreciated}</td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                  </>
                )}
              </div>
            </div>
          </div>
        </>
      )}

      {/* ─── Dispose dialog ─── */}
      {disposing && (
        <Dialog title={`Dispose · ${disposing.asset_no} ${disposing.name}`} onClose={() => setDisposing(null)}>
          <div className="card-body" style={{ display: 'grid', gap: 10 }}>
            <div>Cost: <strong className="mono">{disposing.purchase_cost}</strong></div>
            <div>Accumulated dep: <strong className="mono">{disposing.accumulated_depreciation}</strong></div>
            <div>Book value: <strong className="mono">{(parseFloat(disposing.purchase_cost) - parseFloat(disposing.accumulated_depreciation)).toFixed(2)}</strong></div>
            <label>
              <div className="muted tiny">Proceeds (cash received)</div>
              <input value={proceeds} onChange={(e) => setProceeds(e.target.value)} placeholder="0 if scrapped" />
            </label>
            <label>
              <div className="muted tiny">Proceeds account</div>
              <input value={proceedsAcct} onChange={(e) => setProceedsAcct(e.target.value)} placeholder="1000 cash / 1020 bank" />
            </label>
            <div className="muted tiny">
              If proceeds &gt; book value, gain credits 4300 Gain on Disposal.<br />
              If proceeds &lt; book value, loss debits 5220 Loss on Disposal.
            </div>
            <div style={{ textAlign: 'right' }}>
              <button className="btn btn-primary" disabled={busy} onClick={() => void doDispose()}>
                {busy ? 'Posting…' : 'Post disposal'}
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function Stat({ label, value, mono, bold, color }: { label: string; value: string; mono?: boolean; bold?: boolean; color?: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div style={{
        fontSize: bold ? 22 : 18, fontWeight: bold ? 800 : 700,
        fontFamily: mono ? 'var(--font-mono)' : 'var(--font-mono)', color,
      }}>{value}</div>
    </div>
  );
}

function Dialog({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 99,
    }} onClick={onClose}>
      <div className="card" style={{ minWidth: 480, maxWidth: '90vw' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd"><h3>{title}</h3><button className="btn tiny" onClick={onClose}>Close</button></div>
        {children}
      </div>
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
