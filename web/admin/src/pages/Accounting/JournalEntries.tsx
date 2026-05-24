// Journal entries page — list with filters + create modal for manual
// entries (maker side) + per-row Approve / Reject (checker side).
//
// Manual entries land as pending_approval. A different user clicks
// Approve to post; clicking Reject closes the entry with a reason.
// The backend enforces maker ≠ checker.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  approveJournalEntry,
  createJournalEntry,
  getInboxStatus,
  getJournalEntry,
  listCoA,
  listJournalEntries,
  rejectJournalEntry,
  reverseJournalEntry,
  type CoAAccount,
  type JournalEntry,
  type JournalEntryStatus,
  type JournalLine,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { Icon } from '../../components/Icon';

const STATUS_LABEL: Record<JournalEntryStatus, string> = {
  draft: 'Draft',
  pending_approval: 'Pending approval',
  posted: 'Posted',
  rejected: 'Rejected',
};
const STATUS_COLOR: Record<JournalEntryStatus, string> = {
  draft: 'var(--fg-3)',
  pending_approval: 'var(--warn)',
  posted: 'var(--pos)',
  rejected: 'var(--neg)',
};

export default function JournalEntriesPage() {
  const { tenant, user } = useAuth();
  const [items, setItems] = useState<JournalEntry[] | null>(null);
  const [filterStatus, setFilterStatus] = useState<string>('');
  const [creating, setCreating] = useState(false);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [info, setInfo] = useState<string | null>(null);
  // PR #7 — when on, the inline Approve/Reject buttons hide; the
  // Create action automatically gates through the workflow and the
  // detail rows show a "Open in Inbox →" deep-link instead.
  const [inboxEnabled, setInboxEnabled] = useState(false);

  async function load() {
    setErr(null);
    try {
      const r = await listJournalEntries({ status: filterStatus || undefined, limit: 100 });
      setItems(r.items);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [filterStatus]);
  useEffect(() => {
    getInboxStatus().then((s) => setInboxEnabled(s.unified_inbox_enabled)).catch(() => {});
  }, []);

  async function onApprove(id: string) {
    if (!confirm('Approve and post this entry? It becomes immutable on success.')) return;
    setErr(null); setInfo(null);
    try {
      const e = await approveJournalEntry(id);
      setInfo(`Posted ${e.entry_no} — ${e.narration}`);
      await load();
    } catch (ex) { setErr(extractErr(ex)); }
  }
  async function onReject(id: string) {
    const reason = prompt('Reason for rejection:') ?? '';
    if (!reason.trim()) return;
    setErr(null); setInfo(null);
    try {
      await rejectJournalEntry(id, reason);
      setInfo('Entry rejected.');
      await load();
    } catch (ex) { setErr(extractErr(ex)); }
  }
  async function onReverse(id: string) {
    if (!confirm('Request a reversal of this posted entry? An inverse-lines draft will be sent to the Board for approval.')) return;
    setErr(null); setInfo(null);
    try {
      const e = await reverseJournalEntry(id);
      setInfo(`Reversal draft created (entry ${e.id.slice(0, 8)}) — sent to Board for approval.`);
      await load();
    } catch (ex) { setErr(extractErr(ex)); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance</div>
          <h1>Journal Entries</h1>
          <div className="page-sub">
            Manual entries go through maker/checker. Auto-posted entries from savings, loans, and
            shares show up here too once the integration phase lands.
          </div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={12} /> New entry
          </button>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
          <label className="muted tiny">Status</label>
          <select value={filterStatus} onChange={(e) => setFilterStatus(e.target.value)}>
            <option value="">All</option>
            <option value="pending_approval">Pending approval</option>
            <option value="posted">Posted</option>
            <option value="rejected">Rejected</option>
          </select>
          <button className="btn btn-sm btn-ghost" onClick={() => void load()}>Refresh</button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {info && <div className="alert alert-info" style={{ marginTop: 12 }}>{info}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body flush">
          {items === null && <div className="empty">Loading…</div>}
          {items !== null && items.length === 0 && <div className="empty">No journal entries yet.</div>}
          {items !== null && items.length > 0 && (
            <table className="tbl">
              <thead>
                <tr>
                  <th>Date</th>
                  <th>Entry #</th>
                  <th>Narration</th>
                  <th>Type</th>
                  <th className="num">Debits</th>
                  <th className="num">Credits</th>
                  <th>Status</th>
                  <th style={{ width: 1 }}></th>
                </tr>
              </thead>
              <tbody>
                {items.map((e) => {
                  const isOwn = e.created_by && user?.id && e.created_by === user.id;
                  return (
                    <tr
                      key={e.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => setSelectedID(e.id)}
                    >
                      <td className="tiny">{e.entry_date.slice(0, 10)}</td>
                      <td className="mono">{e.entry_no ?? <span className="muted">—</span>}</td>
                      <td>{e.narration}</td>
                      <td className="tiny">{e.entry_type}</td>
                      <td className="num">{e.total_debits}</td>
                      <td className="num">{e.total_credits}</td>
                      <td>
                        <span style={{ color: STATUS_COLOR[e.status], fontWeight: 600 }}>
                          {STATUS_LABEL[e.status]}
                        </span>
                      </td>
                      <td onClick={(ev) => ev.stopPropagation()}>
                        {/* Pending entries — under unified inbox we hide
                            the inline Approve/Reject and show a deep-
                            link instead. Sub-threshold entries usually
                            won't sit here long because the seeded
                            condition auto-approves them at creation. */}
                        {e.status === 'pending_approval' && inboxEnabled && (
                          e.workflow_instance_id ? (
                            <a className="btn btn-sm btn-accent" href={`/approvals/${e.workflow_instance_id}`}>
                              Open in Inbox →
                            </a>
                          ) : (
                            <span className="muted tiny">Awaiting workflow…</span>
                          )
                        )}
                        {e.status === 'pending_approval' && !inboxEnabled && (
                          <div className="row" style={{ gap: 4 }}>
                            {!isOwn ? (
                              <button className="btn btn-sm btn-primary" onClick={() => void onApprove(e.id)}>
                                Approve
                              </button>
                            ) : (
                              <span className="muted tiny" title="Maker/checker: you can't approve your own entry.">
                                Awaiting checker
                              </span>
                            )}
                            <button className="btn btn-sm btn-ghost" onClick={() => void onReject(e.id)}>
                              Reject
                            </button>
                          </div>
                        )}
                        {/* Posted entries get a Reverse CTA when inbox
                            is on. Reversals go through journal_reversal
                            (Board only); the inverse draft is created
                            immediately and posts on Board approval. */}
                        {e.status === 'posted' && inboxEnabled && (
                          <button className="btn btn-sm btn-ghost" onClick={() => void onReverse(e.id)}>
                            Request reversal
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {creating && (
        <CreateEntryModal
          onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); void load(); }}
        />
      )}
      {selectedID && (
        <EntryDetailModal id={selectedID} onClose={() => setSelectedID(null)} />
      )}
    </div>
  );
}

// ─────────── Create modal ───────────

function CreateEntryModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const today = new Date().toISOString().slice(0, 10);
  const [entryDate, setEntryDate] = useState(today);
  const [narration, setNarration] = useState('');
  const [lines, setLines] = useState<Array<{ account_code: string; debit: string; credit: string; narration: string }>>([
    { account_code: '', debit: '', credit: '', narration: '' },
    { account_code: '', debit: '', credit: '', narration: '' },
  ]);
  const [accounts, setAccounts] = useState<CoAAccount[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const r = await listCoA(true);
        setAccounts(r.items);
      } catch { /* tolerate; user can still type the code */ }
    })();
  }, []);

  const totals = useMemo(() => {
    let d = 0, c = 0;
    for (const ln of lines) {
      d += parseFloat(ln.debit) || 0;
      c += parseFloat(ln.credit) || 0;
    }
    return { d, c, balanced: d === c && d > 0 };
  }, [lines]);

  async function submit() {
    setErr(null);
    if (!totals.balanced) {
      setErr('Debits and credits must balance and be greater than zero.');
      return;
    }
    setBusy(true);
    try {
      await createJournalEntry({
        entry_date: entryDate, narration,
        entry_type: 'manual',
        lines: lines
          .filter((l) => l.account_code && (l.debit || l.credit))
          .map((l) => ({
            account_code: l.account_code,
            debit: l.debit || undefined,
            credit: l.credit || undefined,
            narration: l.narration || undefined,
          })),
      });
      onCreated();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(false); }
  }

  function updateLine(idx: number, patch: Partial<typeof lines[number]>) {
    setLines((cur) => cur.map((l, i) => i === idx ? { ...l, ...patch } : l));
  }
  function addLine() { setLines((cur) => [...cur, { account_code: '', debit: '', credit: '', narration: '' }]); }
  function removeLine(idx: number) {
    setLines((cur) => cur.length <= 2 ? cur : cur.filter((_, i) => i !== idx));
  }

  return (
    <Modal title="New journal entry" onClose={onClose} wide>
      <div className="grid-2">
        <Field label="Entry date">
          <input type="date" value={entryDate} onChange={(e) => setEntryDate(e.target.value)} />
        </Field>
        <Field label="Narration (required)">
          <input value={narration} onChange={(e) => setNarration(e.target.value)} style={{ width: '100%' }} />
        </Field>
      </div>

      <h4 style={{ margin: '14px 0 6px' }}>Lines</h4>
      <table className="tbl">
        <thead>
          <tr>
            <th style={{ width: 40 }}>#</th>
            <th>Account</th>
            <th className="num" style={{ width: 130 }}>Debit</th>
            <th className="num" style={{ width: 130 }}>Credit</th>
            <th>Narration</th>
            <th style={{ width: 1 }}></th>
          </tr>
        </thead>
        <tbody>
          {lines.map((l, i) => (
            <tr key={i}>
              <td className="tiny">{i + 1}</td>
              <td>
                <input
                  list="coa-codes"
                  value={l.account_code}
                  onChange={(e) => updateLine(i, { account_code: e.target.value })}
                  placeholder="1000"
                  style={{ width: 130, fontFamily: 'var(--font-mono)' }}
                />
                {l.account_code && (
                  <div className="muted tiny">
                    {accounts.find((a) => a.code === l.account_code)?.name ?? 'unknown'}
                  </div>
                )}
              </td>
              <td className="num">
                <input
                  type="number" step="0.01" min="0"
                  value={l.debit}
                  onChange={(e) => updateLine(i, { debit: e.target.value, credit: '' })}
                  style={{ width: 120, textAlign: 'right', fontFamily: 'var(--font-mono)' }}
                />
              </td>
              <td className="num">
                <input
                  type="number" step="0.01" min="0"
                  value={l.credit}
                  onChange={(e) => updateLine(i, { credit: e.target.value, debit: '' })}
                  style={{ width: 120, textAlign: 'right', fontFamily: 'var(--font-mono)' }}
                />
              </td>
              <td>
                <input
                  value={l.narration}
                  onChange={(e) => updateLine(i, { narration: e.target.value })}
                  placeholder="(optional per-line note)"
                  style={{ width: '100%' }}
                />
              </td>
              <td>
                <button className="btn btn-sm btn-ghost" onClick={() => removeLine(i)} disabled={lines.length <= 2}>×</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <datalist id="coa-codes">
        {accounts.map((a) => <option key={a.id} value={a.code}>{a.name}</option>)}
      </datalist>

      <div className="row" style={{ gap: 8, marginTop: 8 }}>
        <button className="btn btn-sm btn-ghost" onClick={addLine}>+ Add line</button>
      </div>

      <div className="row" style={{ gap: 16, marginTop: 12, padding: 10, background: 'var(--surface-2)', borderRadius: 6 }}>
        <div><strong>Debits:</strong> {totals.d.toFixed(2)}</div>
        <div><strong>Credits:</strong> {totals.c.toFixed(2)}</div>
        <div style={{ marginLeft: 'auto' }}>
          {totals.balanced
            ? <span style={{ color: 'var(--pos)', fontWeight: 600 }}>Balanced ✓</span>
            : <span style={{ color: 'var(--neg)', fontWeight: 600 }}>Unbalanced ({(totals.d - totals.c).toFixed(2)})</span>}
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}

      <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 14 }}>
        <button className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button
          className="btn btn-primary"
          disabled={busy || !narration || !totals.balanced}
          onClick={() => void submit()}
        >
          {busy ? 'Submitting…' : 'Submit for approval'}
        </button>
      </div>
    </Modal>
  );
}

// ─────────── Detail modal ───────────

function EntryDetailModal({ id, onClose }: { id: string; onClose: () => void }) {
  const [entry, setEntry] = useState<JournalEntry | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    void (async () => {
      try { setEntry(await getJournalEntry(id)); }
      catch (e) { setErr(extractErr(e)); }
    })();
  }, [id]);
  return (
    <Modal title="Journal entry detail" onClose={onClose} wide>
      {err && <div className="alert alert-error">{err}</div>}
      {!entry && !err && <div className="empty">Loading…</div>}
      {entry && (
        <>
          <div className="row" style={{ gap: 16, flexWrap: 'wrap' }}>
            <Stat label="Entry #" value={entry.entry_no ?? '—'} mono />
            <Stat label="Status" value={STATUS_LABEL[entry.status]} color={STATUS_COLOR[entry.status]} />
            <Stat label="Date" value={entry.entry_date.slice(0, 10)} />
            <Stat label="Type" value={entry.entry_type} />
            <Stat label="Debits" value={entry.total_debits} mono />
            <Stat label="Credits" value={entry.total_credits} mono />
          </div>
          <p className="muted" style={{ marginTop: 14 }}>{entry.narration}</p>
          <table className="tbl" style={{ marginTop: 8 }}>
            <thead>
              <tr>
                <th>#</th><th>Account</th><th className="num">Debit</th><th className="num">Credit</th><th>Narration</th>
              </tr>
            </thead>
            <tbody>
              {(entry.lines ?? []).map((l: JournalLine) => (
                <tr key={l.id}>
                  <td className="tiny">{l.line_no}</td>
                  <td><span className="mono">{l.account_code}</span> · {l.account_name}</td>
                  <td className="num mono">{l.debit !== '0' ? l.debit : ''}</td>
                  <td className="num mono">{l.credit !== '0' ? l.credit : ''}</td>
                  <td className="tiny">{l.narration ?? <span className="muted">—</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {entry.status === 'rejected' && entry.rejection_reason && (
            <div className="alert alert-error" style={{ marginTop: 12 }}>
              <strong>Rejected:</strong> {entry.rejection_reason}
            </div>
          )}
        </>
      )}
    </Modal>
  );
}

// ─────────── Bits ───────────

function Modal({ title, children, onClose, wide }: { title: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: wide ? 860 : 560, maxWidth: '94vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd"><h3 style={{ margin: 0 }}>{title}</h3></div>
        <div className="card-body">{children}</div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

function Stat({ label, value, color, mono }: { label: string; value: string; color?: string; mono?: boolean }) {
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 2 }}>{label}</div>
      <div style={{ fontWeight: 600, color, fontFamily: mono ? 'var(--font-mono)' : undefined }}>{value}</div>
    </div>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
