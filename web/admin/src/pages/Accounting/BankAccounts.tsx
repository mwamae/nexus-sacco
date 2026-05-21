// Bank accounts — registry of the SACCO's real bank accounts, each
// linked to a CoA cash code (1020/1030/1040…). Click an account to
// open its detail page where statements are uploaded and
// reconciliation runs.

import { useEffect, useState } from 'react';
import {
  createBankAccount,
  listBankAccounts,
  type BankAccount,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function BankAccountsPage() {
  const { tenant } = useAuth();
  const [items, setItems] = useState<BankAccount[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // create form
  const [show, setShow] = useState(false);
  const [gl, setGL] = useState('1020');
  const [bankName, setBankName] = useState('');
  const [acctNo, setAcctNo] = useState('');
  const [branch, setBranch] = useState('');
  const [notes, setNotes] = useState('');

  async function load() {
    setErr(null);
    try { setItems((await listBankAccounts()).items); }
    catch (e) { setErr(asMsg(e)); }
  }
  useEffect(() => { void load(); }, []);

  async function create() {
    setErr(null); setBusy(true);
    try {
      await createBankAccount({
        gl_account_code: gl, bank_name: bankName, account_number: acctNo,
        branch: branch || undefined, notes: notes || undefined,
      });
      setBankName(''); setAcctNo(''); setBranch(''); setNotes('');
      setShow(false);
      await load();
    } catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Reconciliation</div>
          <h1>Bank accounts</h1>
          <div className="page-sub">
            Real bank accounts the SACCO holds, each mapped to a chart-of-accounts cash code.
            Upload statements and reconcile against the GL on each account's detail page.
          </div>
        </div>
        <button className="btn btn-primary" onClick={() => setShow(!show)}>
          {show ? 'Cancel' : 'Register account'}
        </button>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      {show && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="card-hd"><h3>New bank account</h3></div>
          <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 12 }}>
            <label>
              <div className="muted tiny">GL account code</div>
              <input value={gl} onChange={(e) => setGL(e.target.value)} placeholder="1020" />
            </label>
            <label>
              <div className="muted tiny">Bank name</div>
              <input value={bankName} onChange={(e) => setBankName(e.target.value)} placeholder="Equity Bank" />
            </label>
            <label>
              <div className="muted tiny">Account number</div>
              <input value={acctNo} onChange={(e) => setAcctNo(e.target.value)} placeholder="1234567890" />
            </label>
            <label>
              <div className="muted tiny">Branch</div>
              <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="Nairobi CBD" />
            </label>
            <label style={{ gridColumn: '1 / span 2' }}>
              <div className="muted tiny">Notes</div>
              <input value={notes} onChange={(e) => setNotes(e.target.value)} />
            </label>
            <div style={{ gridColumn: '1 / span 2', textAlign: 'right' }}>
              <button className="btn btn-primary" onClick={() => void create()} disabled={busy || !bankName || !acctNo}>
                {busy ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Registered accounts</h3>
          <span className="card-sub">{items.length} account{items.length === 1 ? '' : 's'}</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Bank</th>
                <th>Account no</th>
                <th>GL code</th>
                <th>Branch</th>
                <th>Currency</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {items.map((b) => (
                <tr key={b.id} style={{ cursor: 'pointer' }} onClick={() => { window.location.href = `/bank-accounts/${b.id}`; }}>
                  <td>{b.bank_name}</td>
                  <td className="mono">{b.account_number}</td>
                  <td className="mono">{b.gl_account_code}</td>
                  <td>{b.branch ?? <span className="muted">—</span>}</td>
                  <td>{b.currency_code}</td>
                  <td>
                    <span style={{ color: b.is_active ? 'var(--pos)' : 'var(--muted)', fontWeight: 600 }}>
                      {b.is_active ? 'active' : 'inactive'}
                    </span>
                  </td>
                </tr>
              ))}
              {items.length === 0 && (
                <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 18 }}>
                  No bank accounts registered yet.
                </td></tr>
              )}
            </tbody>
          </table>
        </div>
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
