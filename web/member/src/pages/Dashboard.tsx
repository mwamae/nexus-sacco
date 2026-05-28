// Member dashboard — list of my accounts, loans, shares + recent
// transactions. Hits a planned /v1/portal/me endpoint that returns
// the member's consolidated picture; until that endpoint ships the
// scaffold renders a placeholder + the buttons that work.

import { useEffect, useState } from 'react';
import { api } from '../api';

type Me = {
  member: { id: string; member_no: string; full_name: string; email?: string; phone?: string };
  accounts?: any[];
  loans?: any[];
};

export default function Dashboard() {
  const [me, setMe] = useState<Me | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    (async () => {
      try {
        const r = await api.get('/v1/portal/me');
        setMe(r.data?.data ?? r.data);
      } catch (e: any) {
        // Endpoint not yet shipped — show a scaffold message.
        if (e?.response?.status === 404) {
          setErr('Self-service /v1/portal/me endpoint not yet wired. The dashboard skeleton is in place; data plumbing lands in a follow-up.');
        } else {
          setErr(e?.response?.data?.error?.message ?? 'Failed to load profile');
        }
      }
    })();
  }, []);

  return (
    <div>
      <div className="card">
        <h2 style={{ marginTop: 0 }}>Welcome back</h2>
        {err && (
          <div style={{ background: '#fffbe7', border: '1px solid #f0d97a', padding: 10, borderRadius: 4, fontSize: 13 }}>
            {err}
          </div>
        )}
        {me?.member && (
          <>
            <div className="row"><span className="lbl">Member no</span><span className="mono">{me.member.member_no}</span></div>
            <div className="row"><span className="lbl">Name</span><span>{me.member.full_name}</span></div>
            {me.member.email && <div className="row"><span className="lbl">Email</span><span>{me.member.email}</span></div>}
            {me.member.phone && <div className="row"><span className="lbl">Phone</span><span className="mono">{me.member.phone}</span></div>}
          </>
        )}
      </div>

      <div className="card">
        <h3>What you can do today</h3>
        <ul style={{ marginTop: 8, paddingLeft: 18 }}>
          <li><a href="#/calculator">Loan calculator</a> — estimate an installment before applying</li>
          <li>Apply for a loan (deferred — pending /v1/portal/loan-applications)</li>
          <li>Download a loan statement (deferred — pending /v1/portal/loans/{'{'}id{'}'}/statement.pdf)</li>
          <li>Track an application (deferred — same backend slot)</li>
          <li>Respond to a guarantor request (deferred — pending /v1/portal/guarantor-requests)</li>
          <li>Propose a promise-to-pay (deferred — pending /v1/portal/ptps)</li>
        </ul>
      </div>
    </div>
  );
}
