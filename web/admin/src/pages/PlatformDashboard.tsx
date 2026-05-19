import { useEffect, useState, type FormEvent } from 'react';
import { useAuth } from '../auth/AuthContext';
import {
  createTenant,
  listTenants,
  extractError,
  type ApiTenant,
} from '../api/client';
import SecurityCard from '../components/SecurityCard';

export default function PlatformDashboard() {
  const { user } = useAuth();
  const [tenants, setTenants] = useState<ApiTenant[] | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);

  async function reload() {
    setLoadErr(null);
    try {
      setTenants(await listTenants());
    } catch (e) {
      setLoadErr(extractError(e));
    }
  }

  useEffect(() => { void reload(); }, []);

  return (
    <main className="page">
      <div className="eyebrow">Platform · Tenants</div>
      <h1>Tenant administration</h1>
      <p className="muted tiny">Signed in as {user?.email} · platform super-admin</p>

      <div style={{ height: 14 }} />

      <SecurityCard />

      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
          <h3 style={{ margin: 0 }}>All tenants</h3>
          <button className="btn btn-primary" onClick={() => setShowForm((s) => !s)}>
            {showForm ? 'Cancel' : '+ New tenant'}
          </button>
        </div>

        {loadErr && <div className="alert alert-error">{loadErr}</div>}

        {showForm && (
          <NewTenantForm
            onCreated={async () => {
              setShowForm(false);
              await reload();
            }}
          />
        )}

        {!tenants && !loadErr && <p className="muted tiny">Loading…</p>}
        {tenants && tenants.length === 0 && (
          <p className="muted tiny">No tenants yet. Create one to get started.</p>
        )}
        {tenants && tenants.length > 0 && (
          <table className="tbl">
            <thead>
              <tr><th>Slug</th><th>Name</th><th>Kind</th><th>Currency</th><th>Status</th><th>URL</th></tr>
            </thead>
            <tbody>
              {tenants
                .filter((t) => t.slug !== 'platform')
                .map((t) => (
                  <tr key={t.id}>
                    <td className="mono">{t.slug}</td>
                    <td>{t.name}</td>
                    <td>{t.kind}</td>
                    <td className="mono">{t.currency_code}</td>
                    <td><span className={t.status === 'active' ? 'badge badge-pos' : 'badge'}>{t.status}</span></td>
                    <td className="mono">
                      <a href={`//${t.slug}.${import.meta.env.VITE_APP_DOMAIN}:5173`} target="_blank" rel="noreferrer">
                        {t.slug}.{import.meta.env.VITE_APP_DOMAIN}
                      </a>
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        )}
      </div>
    </main>
  );
}

function NewTenantForm({ onCreated }: { onCreated: () => void | Promise<void> }) {
  const [slug, setSlug] = useState('');
  const [name, setName] = useState('');
  const [kind, setKind] = useState('sacco');
  const [country, setCountry] = useState('KE');
  const [currency, setCurrency] = useState('KES');
  const [ownerEmail, setOwnerEmail] = useState('');
  const [ownerName, setOwnerName] = useState('');
  const [ownerPassword, setOwnerPassword] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      await createTenant({
        slug, name, kind,
        country_code: country, currency_code: currency,
        owner_email: ownerEmail, owner_name: ownerName, owner_password: ownerPassword,
      });
      await onCreated();
    } catch (e) {
      setErr(extractError(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} style={{ padding: '12px 0', borderTop: '1px solid var(--border)', marginTop: 4 }}>
      {err && <div className="alert alert-error">{err}</div>}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 14 }}>
        <div className="field">
          <label className="field-label">Slug (subdomain)</label>
          <input className="input mono" required value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="tujenge" />
        </div>
        <div className="field">
          <label className="field-label">Name</label>
          <input className="input" required value={name} onChange={(e) => setName(e.target.value)} placeholder="Tujenge SACCO Society" />
        </div>
        <div className="field">
          <label className="field-label">Kind</label>
          <select className="select" value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="sacco">SACCO</option>
            <option value="microfinance">Microfinance</option>
            <option value="digital_lender">Digital Lender</option>
            <option value="cooperative">Cooperative</option>
            <option value="chama">Chama</option>
          </select>
        </div>
        <div className="field">
          <label className="field-label">Country</label>
          <input className="input mono" required maxLength={2} value={country} onChange={(e) => setCountry(e.target.value.toUpperCase())} />
        </div>
        <div className="field">
          <label className="field-label">Currency</label>
          <input className="input mono" required maxLength={3} value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />
        </div>
        <div className="field"></div>
        <div className="field">
          <label className="field-label">Owner email</label>
          <input className="input" required type="email" value={ownerEmail} onChange={(e) => setOwnerEmail(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label">Owner full name</label>
          <input className="input" required value={ownerName} onChange={(e) => setOwnerName(e.target.value)} />
        </div>
        <div className="field" style={{ gridColumn: 'span 2' }}>
          <label className="field-label">Owner password (≥ 12 chars)</label>
          <input className="input" required minLength={12} type="password" value={ownerPassword} onChange={(e) => setOwnerPassword(e.target.value)} />
        </div>
      </div>
      <button type="submit" className="btn btn-primary" disabled={busy}>
        {busy ? 'Creating…' : 'Create tenant'}
      </button>
    </form>
  );
}
