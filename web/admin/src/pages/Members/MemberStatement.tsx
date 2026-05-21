// Member 360° statement — consolidated view of a member's full
// financial relationship with the SACCO. Pulls shares, deposits,
// loans, and a 50-row cross-module activity feed in a single fetch.

import { useEffect, useState } from 'react';
import { getMemberStatement, type MemberStatement } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

const STATUS_COLOR: Record<string, string> = {
  active: 'var(--pos)',
  pending: '#3b6ab8',
  dormant: '#888',
  closed: 'var(--muted)',
  in_arrears: 'var(--neg)',
  restructured: '#c97a00',
  disbursed: 'var(--pos)',
  settled: 'var(--muted)',
  written_off: 'var(--neg)',
};
const MODULE_COLOR: Record<string, string> = {
  shares: '#3b6ab8',
  deposits: 'var(--pos)',
  loans: '#c97a00',
};
const CLASS_COLOR: Record<string, string> = {
  performing: 'var(--pos)',
  watch: '#d4a017',
  substandard: '#c97a00',
  doubtful: 'var(--neg)',
  loss: 'var(--neg)',
};

export default function MemberStatementPage() {
  const { tenant } = useAuth();
  const parts = window.location.pathname.split('/');
  const memberID = parts[parts.length - 2]; // /members/{id}/statement
  const [data, setData] = useState<MemberStatement | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await getMemberStatement(memberID)); }
    catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line */ }, [memberID]);

  if (err) {
    return (
      <div className="page">
        <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>
      </div>
    );
  }
  if (!data) {
    return <div className="page"><div className="muted">{busy ? 'Loading…' : '—'}</div></div>;
  }

  const pos = parseFloat(data.total_financial_position);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Members · Statement</div>
          <h1>{data.member.full_name}</h1>
          <div className="page-sub">
            <span className="mono">{data.member.member_no}</span> ·
            <span style={{ color: STATUS_COLOR[data.member.status], fontWeight: 600, marginLeft: 6 }}>
              {data.member.status}
            </span>
            {data.member.joined_at && <span style={{ marginLeft: 6 }}>· joined {data.member.joined_at.slice(0, 10)}</span>}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <a className="btn" href={`/members/${memberID}`}>← Profile</a>
          <button className="btn" onClick={() => window.print()}>Print</button>
        </div>
      </div>

      {/* ─── Headline net position ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 18 }}>
          <Stat label="Total deposits" value={data.deposits.total_balance} />
          <Stat label="Share book value" value={data.shares?.book_value ?? '0'} />
          <Stat label="Loan obligations" value={data.loans.total_outstanding} color="var(--neg)" />
          <Stat
            label="Net financial position"
            value={data.total_financial_position}
            bold
            color={pos >= 0 ? 'var(--pos)' : 'var(--neg)'}
          />
        </div>
      </div>

      {/* ─── Contact + identity strip ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd"><h3>Contact</h3></div>
        <div className="card-body" style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
          <Field label="Phone" value={data.member.phone ?? '—'} />
          <Field label="Email" value={data.member.email ?? '—'} />
        </div>
      </div>

      {/* ─── Shares ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Shares</h3>
          {data.shares?.certificate_no && (
            <span className="card-sub">Cert <span className="mono">{data.shares.certificate_no}</span></span>
          )}
        </div>
        <div className="card-body">
          {data.shares ? (
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 18 }}>
              <Stat label="Shares held" value={String(data.shares.shares_held)} />
              <Stat label="Par value" value={data.shares.par_value} />
              <Stat label="Book value" value={data.shares.book_value} bold />
              <Stat
                label="Certificate issued"
                value={data.shares.certificate_issued_at?.slice(0, 10) ?? '—'}
              />
            </div>
          ) : (
            <div className="muted">No share account.</div>
          )}
        </div>
      </div>

      {/* ─── Deposits ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Deposit accounts</h3>
          <span className="card-sub">{data.deposits.account_count} account{data.deposits.account_count === 1 ? '' : 's'}</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>Account no</th><th>Product</th><th>Status</th>
                <th>Opened</th>
                <th className="num">Available</th>
                <th className="num">Balance</th>
              </tr>
            </thead>
            <tbody>
              {data.deposits.accounts.map((a) => (
                <tr key={a.account_id}>
                  <td className="mono">{a.account_no}</td>
                  <td><div>{a.product_name}</div><div className="muted tiny mono">{a.product_code}</div></td>
                  <td><span style={{ color: STATUS_COLOR[a.status], fontWeight: 600 }}>{a.status}</span></td>
                  <td className="mono tiny">{a.opened_at.slice(0, 10)}</td>
                  <td className="num mono">{a.available_balance}</td>
                  <td className="num mono"><strong>{a.balance}</strong></td>
                </tr>
              ))}
              {data.deposits.account_count === 0 && (
                <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 14 }}>No deposit accounts.</td></tr>
              )}
              {data.deposits.account_count > 0 && (
                <tr style={{ background: 'var(--surface-2)', fontWeight: 700 }}>
                  <td colSpan={5}>Total</td>
                  <td className="num mono">{data.deposits.total_balance}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* ─── Loans ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Loans</h3>
          <span className="card-sub">
            {data.loans.active_loans} active · {data.loans.total_loans_ever_taken} ever
          </span>
        </div>
        <div className="card-body" style={{ display: 'grid', gap: 12 }}>
          <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
            <Stat label="Total ever disbursed" value={data.loans.total_disbursed} />
            <Stat label="Currently outstanding" value={data.loans.total_outstanding} color="var(--neg)" bold />
          </div>
          <div className="card-body flush">
            <table className="tbl">
              <thead>
                <tr>
                  <th>Loan no</th><th>Product</th><th>Status</th>
                  <th className="num">Principal</th>
                  <th className="num">P. balance</th>
                  <th className="num">Int. balance</th>
                  <th className="num">DPD</th>
                  <th>Class</th>
                  <th>Next due</th>
                </tr>
              </thead>
              <tbody>
                {data.loans.loans.map((r) => (
                  <tr key={r.loan.id}>
                    <td className="mono">{r.loan.loan_no}</td>
                    <td><div>{r.product_name}</div><div className="muted tiny mono">{r.product_code}</div></td>
                    <td><span style={{ color: STATUS_COLOR[r.loan.status], fontWeight: 600 }}>{r.loan.status}</span></td>
                    <td className="num mono">{r.loan.principal}</td>
                    <td className="num mono">{r.loan.principal_balance}</td>
                    <td className="num mono">{r.loan.interest_balance}</td>
                    <td className="num mono">{r.loan.days_past_due}</td>
                    <td><span style={{ color: CLASS_COLOR[r.loan.arrears_classification], fontWeight: 600 }}>{r.loan.arrears_classification}</span></td>
                    <td className="mono tiny">{r.loan.next_installment_due_at?.slice(0, 10) ?? '—'}</td>
                  </tr>
                ))}
                {data.loans.loans.length === 0 && (
                  <tr><td colSpan={9} className="muted" style={{ textAlign: 'center', padding: 14 }}>No loans on record.</td></tr>
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {/* ─── Recent activity ─── */}
      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-hd">
          <h3>Recent activity</h3>
          <span className="card-sub">{data.recent_activity.length} latest entries</span>
        </div>
        <div className="card-body flush">
          <table className="tbl">
            <thead>
              <tr>
                <th>When</th><th>Module</th><th>Type</th><th>Description</th>
                <th>Ref</th>
                <th className="num">Amount</th>
              </tr>
            </thead>
            <tbody>
              {data.recent_activity.map((a) => (
                <tr key={a.txn_no}>
                  <td className="mono tiny">{new Date(a.posted_at).toLocaleString()}</td>
                  <td><span style={{ color: MODULE_COLOR[a.module], fontWeight: 600 }}>{a.module}</span></td>
                  <td className="mono tiny">{a.type}</td>
                  <td>
                    <div>{a.description}</div>
                    {a.narration && <div className="muted tiny">{a.narration}</div>}
                  </td>
                  <td className="mono tiny">{a.reference || <span className="muted">—</span>}</td>
                  <td className="num mono" style={{
                    color: parseFloat(a.amount) > 0 ? 'var(--pos)' : parseFloat(a.amount) < 0 ? 'var(--neg)' : 'var(--muted)',
                    fontWeight: 700,
                  }}>
                    {a.amount}
                  </td>
                </tr>
              ))}
              {data.recent_activity.length === 0 && (
                <tr><td colSpan={6} className="muted" style={{ textAlign: 'center', padding: 14 }}>No activity on record.</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="muted tiny" style={{ marginTop: 12, textAlign: 'right' }}>
        Generated {new Date(data.generated_at).toLocaleString()}
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

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="muted tiny">{label}</div>
      <div>{value}</div>
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
