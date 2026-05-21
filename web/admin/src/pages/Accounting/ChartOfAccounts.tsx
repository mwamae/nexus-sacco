// Chart of Accounts admin — list, add custom account, edit name +
// active flag on tenant-created accounts. System-locked accounts are
// shown but the Edit button is suppressed (the backend rejects PATCHes
// on them anyway).

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  createCoAAccount, listCoA, updateCoAAccount,
  type AccountClass, type CoAAccount, type NormalBalance,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';
import { Icon } from '../../components/Icon';

const CLASS_LABEL: Record<AccountClass, string> = {
  asset: 'Assets', liability: 'Liabilities', equity: 'Equity',
  income: 'Income', expense: 'Expenses',
};
const CLASS_ORDER: AccountClass[] = ['asset', 'liability', 'equity', 'income', 'expense'];

export default function ChartOfAccountsPage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<CoAAccount[] | null>(null);
  const [filter, setFilter] = useState('');
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<CoAAccount | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await listCoA(false);
      setItems(r.items);
    } catch (e) {
      setErr(extractErr(e));
    }
  }
  useEffect(() => { void load(); }, []);

  const grouped = useMemo(() => {
    if (!items) return null;
    const q = filter.toLowerCase().trim();
    const filtered = q
      ? items.filter((a) => a.code.toLowerCase().includes(q) || a.name.toLowerCase().includes(q))
      : items;
    const m: Record<string, CoAAccount[]> = {};
    for (const a of filtered) (m[a.class] ??= []).push(a);
    return m;
  }, [items, filter]);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance</div>
          <h1>Chart of Accounts</h1>
          <div className="page-sub">
            Every account that the General Ledger can post to. {items?.length ?? 0} accounts on file —
            71 are SACCO defaults (locked) and the rest are yours.
          </div>
        </div>
        <div className="page-hd-actions">
          <button className="btn btn-primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={12} /> New account
          </button>
        </div>
      </div>

      <div className="card">
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
          <input
            type="search"
            placeholder="Filter by code or name"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={{ width: 320 }}
          />
          <button className="btn btn-sm btn-ghost" onClick={() => void load()}>Refresh</button>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}
      {items === null && <div className="empty">Loading…</div>}

      {grouped && CLASS_ORDER.map((cls) => {
        const rows = grouped[cls] ?? [];
        if (rows.length === 0) return null;
        return (
          <div key={cls} className="card" style={{ marginTop: 12 }}>
            <div className="card-hd">
              <h3>{CLASS_LABEL[cls]}</h3>
              <span className="card-sub">{rows.length} accounts</span>
            </div>
            <div className="card-body flush">
              <table className="tbl">
                <thead>
                  <tr>
                    <th style={{ width: 100 }}>Code</th>
                    <th>Name</th>
                    <th>Type</th>
                    <th>Normal balance</th>
                    <th>Active</th>
                    <th style={{ width: 1 }}></th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((a) => (
                    <tr key={a.id}>
                      <td className="mono">{a.code}</td>
                      <td>
                        {a.name}
                        {a.is_system_locked && (
                          <span className="muted tiny" style={{ marginLeft: 8 }}>locked</span>
                        )}
                      </td>
                      <td className="tiny">{a.type}</td>
                      <td className="tiny">{a.normal_balance}</td>
                      <td>
                        <span style={{
                          color: a.is_active ? 'var(--pos)' : 'var(--fg-3)',
                          fontWeight: 600,
                        }}>{a.is_active ? 'On' : 'Off'}</span>
                      </td>
                      <td>
                        {!a.is_system_locked && (
                          <button className="btn btn-sm btn-ghost" onClick={() => setEditing(a)}>Edit</button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        );
      })}

      {creating && (
        <CreateAccountModal
          onClose={() => setCreating(false)}
          onCreated={() => { setCreating(false); void load(); }}
        />
      )}
      {editing && (
        <EditAccountModal
          account={editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); void load(); }}
        />
      )}
    </div>
  );
}

function CreateAccountModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [code, setCode] = useState('');
  const [name, setName] = useState('');
  const [klass, setKlass] = useState<AccountClass>('expense');
  const [type, setType] = useState('admin_expense');
  const [parentCode, setParentCode] = useState('');
  const [nb, setNB] = useState<NormalBalance>('debit');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setErr(null); setBusy(true);
    try {
      await createCoAAccount({
        code, name, class: klass, type,
        parent_code: parentCode || undefined,
        normal_balance: nb, is_active: true,
      });
      onCreated();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(false); }
  }
  return (
    <Modal title="Add account" onClose={onClose}>
      <div className="form-grid">
        <Field label="Account code" hint="Numeric, e.g. 5910">
          <input value={code} onChange={(e) => setCode(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
        </Field>
        <Field label="Account name">
          <input value={name} onChange={(e) => setName(e.target.value)} style={{ width: '100%' }} />
        </Field>
        <Field label="Class">
          <select value={klass} onChange={(e) => {
            const k = e.target.value as AccountClass;
            setKlass(k);
            // Sensible default normal-balance per class.
            setNB(k === 'asset' || k === 'expense' ? 'debit' : 'credit');
          }} style={{ width: '100%' }}>
            {CLASS_ORDER.map((c) => <option key={c} value={c}>{CLASS_LABEL[c]}</option>)}
          </select>
        </Field>
        <Field label="Type" hint="Free-text refinement (e.g. fee_income, current_asset)">
          <input value={type} onChange={(e) => setType(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
        </Field>
        <Field label="Normal balance">
          <select value={nb} onChange={(e) => setNB(e.target.value as NormalBalance)}>
            <option value="debit">Debit</option>
            <option value="credit">Credit</option>
          </select>
        </Field>
        <Field label="Parent account code (optional)" hint="For hierarchical roll-up reporting">
          <input value={parentCode} onChange={(e) => setParentCode(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
        </Field>
      </div>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
        <button className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn btn-primary" disabled={busy || !code || !name} onClick={() => void submit()}>
          {busy ? 'Saving…' : 'Create account'}
        </button>
      </div>
    </Modal>
  );
}

function EditAccountModal({ account, onClose, onSaved }: { account: CoAAccount; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(account.name);
  const [type, setType] = useState(account.type);
  const [isActive, setIsActive] = useState(account.is_active);
  const [parentCode, setParentCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  async function submit() {
    setErr(null); setBusy(true);
    try {
      await updateCoAAccount(account.id, {
        name, type, parent_code: parentCode || undefined, is_active: isActive,
      });
      onSaved();
    } catch (e) { setErr(extractErr(e)); }
    finally { setBusy(false); }
  }
  return (
    <Modal title={`Edit account ${account.code}`} onClose={onClose}>
      <div className="form-grid">
        <Field label="Code"><div className="mono">{account.code}</div></Field>
        <Field label="Class"><div>{CLASS_LABEL[account.class]} · {account.normal_balance}</div></Field>
        <Field label="Name"><input value={name} onChange={(e) => setName(e.target.value)} style={{ width: '100%' }} /></Field>
        <Field label="Type"><input value={type} onChange={(e) => setType(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} /></Field>
        <Field label="New parent code (leave blank to keep)">
          <input value={parentCode} onChange={(e) => setParentCode(e.target.value)} style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
        </Field>
        <Field label="Active">
          <label className="row" style={{ gap: 6 }}>
            <input type="checkbox" checked={isActive} onChange={(e) => setIsActive(e.target.checked)} />
            {isActive ? 'Available for posting' : 'Deactivated (no new entries can reference it)'}
          </label>
        </Field>
      </div>
      {err && <div className="alert alert-error" style={{ marginTop: 8 }}>{err}</div>}
      <div className="row" style={{ gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
        <button className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="btn btn-primary" disabled={busy} onClick={() => void submit()}>
          {busy ? 'Saving…' : 'Save'}
        </button>
      </div>
    </Modal>
  );
}

// ─────────── Bits ───────────

function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  return (
    <div
      style={{ position: 'fixed', inset: 0, zIndex: 1000, background: 'rgba(0,0,0,.45)', display: 'grid', placeItems: 'center' }}
      onClick={onClose}
    >
      <div className="card" style={{ width: 560, maxWidth: '92vw', maxHeight: '92vh', overflow: 'auto' }} onClick={(e) => e.stopPropagation()}>
        <div className="card-hd"><h3 style={{ margin: 0 }}>{title}</h3></div>
        <div className="card-body">{children}</div>
      </div>
    </div>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div className="muted tiny" style={{ marginTop: 4 }}>{hint}</div>}
    </label>
  );
}

function extractErr(e: unknown): string {
  if (e && typeof e === 'object' && 'response' in e) {
    const r = (e as { response?: { data?: { error?: { message?: string } } } }).response;
    if (r?.data?.error?.message) return r.data.error.message;
  }
  return e instanceof Error ? e.message : 'Unknown error';
}
