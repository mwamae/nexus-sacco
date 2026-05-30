// DSID Phase 2.1 — member Statements tab.
//
// Four cards: deposits / shares / interest / dividend. Each has a period
// picker, "Generate PDF" (opens in new tab via authed-blob fetch), and
// "Email to member" (POSTs the email endpoint; PDF rendered + attached
// on the notification service side).

import { useState } from 'react';
import {
  depositStatementURL,
  shareStatementURL,
  interestStatementURL,
  dividendStatementURL,
  emailMemberStatement,
  openAuthedFile,
} from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

type Props = {
  counterpartyId: string;
  memberEmail?: string;
};

function thisQuarter(): { from: string; to: string; label: string } {
  const now = new Date();
  const q = Math.floor(now.getMonth() / 3);
  const fromY = now.getFullYear();
  const fromM = q * 3;
  const from = new Date(Date.UTC(fromY, fromM, 1));
  const to = new Date(Date.UTC(fromY, fromM + 3, 0));
  const ymd = (d: Date) => d.toISOString().slice(0, 10);
  return { from: ymd(from), to: ymd(to), label: `${fromY}-Q${q + 1}` };
}

function currentFY(): string {
  const now = new Date();
  const y = now.getMonth() >= 6 /* July onwards */ ? now.getFullYear() : now.getFullYear() - 1;
  return `${y}-${y + 1}`;
}

export default function StatementsTab({ counterpartyId, memberEmail }: Props) {
  const { hasPermission } = useAuth();
  const canEmail = hasPermission('members:view');
  const q = thisQuarter();
  const fy = currentFY();

  const [depFrom, setDepFrom] = useState(q.from);
  const [depTo, setDepTo] = useState(q.to);
  const [shareFY, setShareFY] = useState(fy);
  const [interestFY, setInterestFY] = useState(fy);
  const [dividendFY, setDividendFY] = useState(fy);

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))', gap: 14 }}>
      <StatementCard
        title="Deposit statement"
        subtitle="Per-account transactions across a date range."
        accent="#2c5282"
      >
        <div style={{ display: 'flex', gap: 8, marginBottom: 10 }}>
          <label style={{ flex: 1 }}>
            <div className="muted tiny" style={{ marginBottom: 2 }}>From</div>
            <input className="input" type="date" value={depFrom} onChange={(e) => setDepFrom(e.target.value)} />
          </label>
          <label style={{ flex: 1 }}>
            <div className="muted tiny" style={{ marginBottom: 2 }}>To</div>
            <input className="input" type="date" value={depTo} onChange={(e) => setDepTo(e.target.value)} />
          </label>
        </div>
        <ActionRow
          onGenerate={() => openAuthedFile(depositStatementURL(counterpartyId, depFrom, depTo))}
          onEmail={canEmail ? () => emailMemberStatement(counterpartyId, {
            kind: 'deposits',
            period: `${depFrom}..${depTo}`,
          }) : null}
          memberEmail={memberEmail}
        />
      </StatementCard>

      <StatementCard
        title="Share statement"
        subtitle="Opening + closing shares + transactions for a financial year."
        accent="#146c43"
      >
        <FYInput label="Financial year" value={shareFY} onChange={setShareFY} />
        <ActionRow
          onGenerate={() => openAuthedFile(shareStatementURL(counterpartyId, shareFY))}
          onEmail={canEmail ? () => emailMemberStatement(counterpartyId, { kind: 'shares', period: shareFY }) : null}
          memberEmail={memberEmail}
        />
      </StatementCard>

      <StatementCard
        title="Interest statement"
        subtitle="Per-account interest + WHT + payout for the FY."
        accent="#8a6d00"
      >
        <FYInput label="Financial year" value={interestFY} onChange={setInterestFY} />
        <ActionRow
          onGenerate={() => openAuthedFile(interestStatementURL(counterpartyId, interestFY))}
          onEmail={canEmail ? () => emailMemberStatement(counterpartyId, { kind: 'interest', period: interestFY }) : null}
          memberEmail={memberEmail}
        />
      </StatementCard>

      <StatementCard
        title="Dividend statement"
        subtitle="Capital basis + gross/WHT/net dividend for the FY."
        accent="#b42318"
      >
        <FYInput label="Financial year" value={dividendFY} onChange={setDividendFY} />
        <ActionRow
          onGenerate={() => openAuthedFile(dividendStatementURL(counterpartyId, dividendFY))}
          onEmail={canEmail ? () => emailMemberStatement(counterpartyId, { kind: 'dividend', period: dividendFY }) : null}
          memberEmail={memberEmail}
        />
      </StatementCard>
    </div>
  );
}

function StatementCard({ title, subtitle, accent, children }: { title: string; subtitle: string; accent: string; children: React.ReactNode }) {
  return (
    <div className="card" style={{ padding: 14, borderLeft: `4px solid ${accent}` }}>
      <h3 style={{ marginTop: 0, marginBottom: 4, color: accent }}>{title}</h3>
      <div className="muted tiny" style={{ marginBottom: 12 }}>{subtitle}</div>
      {children}
    </div>
  );
}

function FYInput({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label style={{ display: 'block', marginBottom: 10 }}>
      <div className="muted tiny" style={{ marginBottom: 2 }}>{label}</div>
      <input className="input" value={value} onChange={(e) => onChange(e.target.value)} placeholder="2025-2026" />
      <div className="muted tiny" style={{ marginTop: 2 }}>Format: <code>YYYY-YYYY</code> (July → June)</div>
    </label>
  );
}

function ActionRow({ onGenerate, onEmail, memberEmail }: {
  onGenerate: () => Promise<void>;
  onEmail: null | (() => Promise<unknown>);
  memberEmail?: string;
}) {
  const [busy, setBusy] = useState<'generate' | 'email' | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function doGenerate() {
    setBusy('generate'); setErr(null); setMsg(null);
    try { await onGenerate(); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Failed to generate.'); }
    finally { setBusy(null); }
  }
  async function doEmail() {
    if (!onEmail) return;
    if (!memberEmail) { setErr('Member has no registered email.'); return; }
    setBusy('email'); setErr(null); setMsg(null);
    try { await onEmail(); setMsg(`Queued for ${memberEmail}`); }
    catch (e: any) { setErr(e?.response?.data?.error?.message || e?.message || 'Email failed.'); }
    finally { setBusy(null); }
  }
  return (
    <>
      <div style={{ display: 'flex', gap: 6 }}>
        <button className="btn btn-sm btn-primary" disabled={busy !== null} onClick={() => void doGenerate()}>
          {busy === 'generate' ? 'Generating…' : 'Generate PDF ↗'}
        </button>
        {onEmail && (
          <button className="btn btn-sm" disabled={busy !== null || !memberEmail} onClick={() => void doEmail()}>
            {busy === 'email' ? 'Sending…' : '✉ Email to member'}
          </button>
        )}
      </div>
      {msg && <div className="muted tiny" style={{ marginTop: 6, color: 'var(--success-fg, #146c43)' }}>{msg}</div>}
      {err && <div className="muted tiny" style={{ marginTop: 6, color: 'var(--danger-fg, #b42318)' }}>{err}</div>}
      {!memberEmail && onEmail && (
        <div className="muted tiny" style={{ marginTop: 6 }}>No registered email — only generate available.</div>
      )}
    </>
  );
}
