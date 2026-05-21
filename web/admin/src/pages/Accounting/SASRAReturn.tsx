// SASRA Quarterly Return — the canonical submission template.
// Pulls position, capital, loan portfolio, deposits, ratios + compliance
// flags from the GL at an as-of date.

import { useEffect, useState } from 'react';
import { downloadReport, sasraReturn, type SASRAReturn } from '../../api/client';
import { useAuth } from '../../auth/AuthContext';

export default function SASRAReturnPage() {
  const { tenant } = useAuth();
  const today = new Date().toISOString().slice(0, 10);
  const [asOf, setAsOf] = useState(today);
  const [data, setData] = useState<SASRAReturn | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    setErr(null); setBusy(true);
    try { setData(await sasraReturn(asOf)); }
    catch (e) { setErr(asMsg(e)); }
    finally { setBusy(false); }
  }
  useEffect(() => { void load(); /* eslint-disable-next-line */ }, []);

  return (
    <div className="page">
      <div className="page-hd">
        <div>
          <div className="eyebrow">{tenant?.name} · Finance · Regulatory</div>
          <h1>SASRA Quarterly Return</h1>
          <div className="page-sub">
            Statement of financial position summary, capital, loan portfolio, deposits and the
            six SASRA prudential ratios. All numbers derived from posted GL through the as-of date.
          </div>
        </div>
      </div>

      {err && <div className="alert alert-error" style={{ marginTop: 12 }}>{err}</div>}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="card-body" style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
          <label>
            <div className="muted tiny">As of</div>
            <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} />
          </label>
          <button className="btn btn-primary" disabled={busy} onClick={() => void load()}>
            {busy ? 'Loading…' : 'Run'}
          </button>
          <div style={{ display: 'flex', gap: 6 }}>
            <button className="btn" disabled={!data} onClick={() => void downloadReport('sasra-return', { as_of: asOf })}>
              Export XLSX
            </button>
            <button className="btn" onClick={() => window.print()}>Print</button>
          </div>
          {data && (
            <div style={{ marginLeft: 'auto' }}>
              <div className="muted tiny">Overall compliance</div>
              <div style={{ fontSize: 22, fontWeight: 800, color: data.all_compliant ? 'var(--pos)' : 'var(--neg)' }}>
                {data.all_compliant ? '✓ All ratios compliant' : '✗ Issues — see below'}
              </div>
            </div>
          )}
        </div>
      </div>

      {data && (
        <>
          {/* ─── Statement of Financial Position summary ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Statement of Financial Position</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 18 }}>
              <Stat label="Total assets" value={data.position.total_assets} bold />
              <Stat label="Total liabilities" value={data.position.total_liabilities} />
              <Stat label="Total equity" value={data.position.total_equity} />
            </div>
          </div>

          {/* ─── Income summary ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd">
              <h3>Income (YTD)</h3>
              <span className="card-sub">
                {data.income_statement.from_date.slice(0, 10)} → {data.income_statement.to_date.slice(0, 10)}
              </span>
            </div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 18 }}>
              <Stat label="Total income" value={data.income_statement.total_income} color="var(--pos)" />
              <Stat label="Total expense" value={data.income_statement.total_expense} color="var(--neg)" />
              <Stat
                label="Net surplus / (deficit)"
                value={data.income_statement.net_surplus}
                bold
                color={parseFloat(data.income_statement.net_surplus) >= 0 ? 'var(--pos)' : 'var(--neg)'}
              />
            </div>
          </div>

          {/* ─── Capital ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Capital</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: 18 }}>
              <Section title="Components">
                <Row label="Member share capital (3000)" value={data.capital.share_capital} />
                <Row label="Retained earnings + unclosed P&L" value={data.capital.retained_earnings} />
                <Row label="Statutory reserve (3020)" value={data.capital.statutory_reserve} />
                <Row label="General reserves (3030)" value={data.capital.general_reserves} />
                <Row label="Institutional capital (3050)" value={data.capital.institutional_capital_acct} />
                <Row label="Less: Intangible assets (1600)" value={data.capital.intangible_assets} negative />
              </Section>
              <Section title="Aggregates">
                <Row label="Core capital" value={data.capital.core_capital} bold />
                <Row label="Institutional capital total" value={data.capital.institutional_capital} bold />
              </Section>
            </div>
          </div>

          {/* ─── Loan portfolio ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Loan portfolio</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(5, 1fr)', gap: 18 }}>
              <Stat label="Gross loans (1100)" value={data.loan_portfolio.gross_loans} />
              <Stat label="Interest receivable (1110)" value={data.loan_portfolio.interest_receivable} />
              <Stat label="Provisions (1120)" value={data.loan_portfolio.provisions} color="var(--neg)" />
              <Stat label="Net loans" value={data.loan_portfolio.net_loans} bold />
              <Stat label="Provision coverage" value={`${data.loan_portfolio.provision_coverage_pct}%`} />
            </div>
          </div>

          {/* ─── Deposits + Borrowings + Liquidity ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd"><h3>Funding & liquidity</h3></div>
            <div className="card-body" style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 18 }}>
              <Stat label="Member savings" value={data.deposits.member_savings} />
              <Stat label="Fixed deposits" value={data.deposits.fixed_deposits} />
              <Stat label="External borrowings" value={data.borrowings} />
              <Stat label="Liquid assets" value={data.liquid_assets} bold />
            </div>
          </div>

          {/* ─── Ratios ─── */}
          <div className="card" style={{ marginTop: 12 }}>
            <div className="card-hd">
              <h3>SASRA prudential ratios</h3>
              <span className="card-sub">{data.ratios.length} ratios</span>
            </div>
            <div className="card-body flush">
              <table className="tbl">
                <thead>
                  <tr>
                    <th>Ratio</th>
                    <th className="num">Numerator</th>
                    <th className="num">Denominator</th>
                    <th className="num">Result</th>
                    <th>Threshold</th>
                    <th>Compliant?</th>
                  </tr>
                </thead>
                <tbody>
                  {data.ratios.map((r) => (
                    <tr key={r.label}>
                      <td>
                        <div>{r.label}</div>
                        {r.notes && <div className="muted tiny">{r.notes}</div>}
                      </td>
                      <td className="num mono">{r.numerator}</td>
                      <td className="num mono">{r.denominator}</td>
                      <td className="num mono"><strong>{r.ratio}%</strong></td>
                      <td>
                        <span className="mono">{r.operator === 'min' ? '≥' : '≤'} {r.threshold}%</span>
                      </td>
                      <td>
                        <span style={{ color: r.compliant ? 'var(--pos)' : 'var(--neg)', fontWeight: 700 }}>
                          {r.compliant ? '✓' : '✗ breach'}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
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

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="muted tiny" style={{ marginBottom: 6 }}>{title}</div>
      {children}
    </div>
  );
}

function Row({ label, value, bold, negative }: { label: string; value: string; bold?: boolean; negative?: boolean }) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', padding: '4px 0', borderBottom: '1px solid var(--surface-2)' }}>
      <span style={{ fontWeight: bold ? 700 : undefined }}>{label}</span>
      <span className="mono" style={{
        fontWeight: bold ? 800 : 600,
        color: negative ? 'var(--neg)' : undefined,
      }}>
        {negative ? `(${value})` : value}
      </span>
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
