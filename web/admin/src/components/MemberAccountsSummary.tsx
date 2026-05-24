// Compact "Accounts" snapshot — sits above the unified ledger panel
// on the Member Detail → Accounts tab. Lists every savings, loan, and
// share account this member holds with its current balance + a
// click-through to the source module.
//
// Distinct from MemberAccountsPanel (which is the action-rich editor):
// this is read-only, fits all three account types in one card
// including loans (which the editor doesn't surface), and serves as
// the "at a glance" entry point before the officer drills into the
// editor or scrolls down to the ledger.

import { useCallback, useEffect, useState } from 'react';
import {
  getDepositAccountsByMember,
  getInboxStatus,
  getMemberLoanHistory,
  getShareAccountByMember,
  type Loan,
  type MemberDepositItem,
  type ShareAccountView,
} from '../api/client';
import { AsyncPanel, isTimeoutError } from './AsyncPanel';
import { Badge, StatusBadge } from './Badge';

type Bundle = {
  deposits: MemberDepositItem[];
  loans: Array<{ loan: Loan; product_code: string; product_name: string }>;
  share: ShareAccountView | null;
};

function fmtMoney(s: string | number | undefined | null): string {
  if (s === undefined || s === null) return '0.00';
  const n = typeof s === 'number' ? s : parseFloat(s);
  if (!isFinite(n)) return '0.00';
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

export function MemberAccountsSummary({
  counterpartyId,
  currency,
}: {
  counterpartyId: string;
  currency: string;
}) {
  // Three parallel fetches via Promise.all — same shape as the
  // FinancialKPIStrip in MemberProfile, but returning the raw rows
  // instead of aggregates so we can render per-account links.
  const fetcher = useCallback(async (): Promise<Bundle> => {
    const [deposits, loanHistory, share] = await Promise.all([
      getDepositAccountsByMember(counterpartyId),
      getMemberLoanHistory(counterpartyId),
      getShareAccountByMember(counterpartyId).catch(() => null),
    ]);
    return { deposits, loans: loanHistory.loans ?? [], share };
  }, [counterpartyId]);

  // PR 5: BOSA/FOSA tiles + segment-grouped rows are gated on the
  // tenant flag so flag-off tenants see the legacy single-list view.
  const [segmentEnabled, setSegmentEnabled] = useState<boolean | null>(null);
  useEffect(() => {
    getInboxStatus()
      .then((s) => setSegmentEnabled(s.bosa_fosa_enabled))
      .catch(() => setSegmentEnabled(false));
  }, []);

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Accounts</h3>
        <span className="card-sub">
          All savings, loan, and share accounts on file · click an account to open it in its module
        </span>
      </div>
      <div className="card-body flush">
        <AsyncPanel
          fetcher={fetcher}
          deps={[counterpartyId]}
          isEmpty={(b) => b.deposits.length === 0 && b.loans.length === 0 && !b.share}
          empty={(
            <div className="empty" style={{ padding: 20 }}>
              No accounts on file for this member yet.
            </div>
          )}
          errorTitle="Couldn't load accounts"
          errorMessage={(err) => isTimeoutError(err)
            ? "The savings service didn't respond in time. Try again."
            : "We couldn't fetch this member's accounts."}
          skeleton={<SummarySkeleton />}
        >
          {(b) => (
            <>
              {segmentEnabled && <TileStrip bundle={b} currency={currency} />}
              <SummaryTable bundle={b} counterpartyId={counterpartyId} currency={currency} segmentEnabled={!!segmentEnabled} />
            </>
          )}
        </AsyncPanel>
      </div>
    </div>
  );
}

// PR 5: BOSA / FOSA / Share capital / Net position tiles at the top
// of the Accounts card. Sums are derived per-render from the same
// bundle the table reads, so they stay in sync with the rows below
// without a separate fetch.
function TileStrip({ bundle, currency }: { bundle: Bundle; currency: string }) {
  const bosa = bundle.deposits
    .filter((d) => d.product.segment === 'bosa')
    .reduce((s, d) => s + parseFloat(d.account.current_balance || '0'), 0);
  const fosa = bundle.deposits
    .filter((d) => d.product.segment === 'fosa')
    .reduce((s, d) => s + parseFloat(d.account.current_balance || '0'), 0);
  const shares = bundle.share ? parseFloat(bundle.share.account.total_value || '0') : 0;
  const loans = bundle.loans.reduce((s, r) => s + parseFloat(r.loan.principal_balance || '0'), 0);
  // Net position = BOSA + FOSA + Shares − outstanding loan principal.
  // Same shape as the FinanceDashboard's net-equity-per-member view;
  // simple, unweighted, doesn't pretend to be a credit metric.
  const net = bosa + fosa + shares - loans;
  return (
    <div
      style={{
        display: 'grid',
        gridTemplateColumns: 'repeat(4, 1fr)',
        gap: 12,
        padding: '14px 16px 4px',
      }}
    >
      <Tile
        label="BOSA deposits"
        value={`${currency} ${fmtMoney(bosa)}`}
        hint="secures loans · redeemable on exit"
        chip={<Badge tone="warn">BOSA</Badge>}
      />
      <Tile
        label="FOSA savings"
        value={`${currency} ${fmtMoney(fosa)}`}
        hint="withdrawable balance"
        chip={<Badge tone="neutral">FOSA</Badge>}
      />
      <Tile
        label="Share capital"
        value={`${currency} ${fmtMoney(shares)}`}
        hint="equity · redeemable on exit"
      />
      <Tile
        label="Net position"
        value={`${currency} ${fmtMoney(net)}`}
        hint={loans > 0 ? `after ${currency} ${fmtMoney(loans)} loan principal` : 'no active loans'}
      />
    </div>
  );
}

function Tile({ label, value, hint, chip }: { label: string; value: string; hint: string; chip?: React.ReactNode }) {
  return (
    <div>
      <div className="muted tiny" style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
        {label} {chip}
      </div>
      <div style={{ fontSize: 18, fontWeight: 800, fontFamily: 'var(--font-mono)', marginTop: 2 }}>{value}</div>
      <div className="muted tiny" style={{ marginTop: 2 }}>{hint}</div>
    </div>
  );
}

function SummaryTable({
  bundle,
  counterpartyId,
  currency,
  segmentEnabled,
}: {
  bundle: Bundle;
  counterpartyId: string;
  currency: string;
  segmentEnabled: boolean;
}) {
  // PR 5: split deposits by segment when the flag is on so the
  // BOSA bond is visually separated from withdrawable FOSA. Order
  // intentionally: shares → BOSA → FOSA → loans, mirroring the
  // funding-mix progression on the SASRA return UI.
  const bosa = bundle.deposits.filter((d) => d.product.segment === 'bosa');
  const fosa = bundle.deposits.filter((d) => d.product.segment === 'fosa');

  return (
    <table className="tbl">
      <thead>
        <tr>
          <th>Type</th>
          <th>Account</th>
          <th>Status</th>
          <th style={{ textAlign: 'right' }}>Balance</th>
          <th style={{ width: 1 }}></th>
        </tr>
      </thead>
      <tbody>
        {bundle.share && (
          <tr>
            <td><strong>Shares</strong></td>
            <td>
              <div className="tiny-mono">{bundle.share.account.account_no}</div>
              <div className="muted tiny">
                {bundle.share.account.shares_held.toLocaleString()} shares @ {currency} {fmtMoney(bundle.share.account.par_value_at_open)}
              </div>
            </td>
            <td><StatusBadge status={bundle.share.account.status} /></td>
            <td className="mono num" style={{ textAlign: 'right' }}>
              {currency} {fmtMoney(bundle.share.account.total_value)}
            </td>
            <td>
              <a className="btn btn-sm" href={`/shares?member=${counterpartyId}`}>Open →</a>
            </td>
          </tr>
        )}

        {segmentEnabled && bosa.length > 0 && (
          <tr>
            <td colSpan={5} className="muted tiny" style={{ background: 'var(--surface-2)', textTransform: 'uppercase', letterSpacing: '.5px', padding: '6px 12px' }}>
              BOSA · Member deposits
            </td>
          </tr>
        )}
        {(segmentEnabled ? bosa : []).map((d) => (
          <DepositRow key={d.account.id} d={d} counterpartyId={counterpartyId} currency={currency} segmentEnabled />
        ))}

        {segmentEnabled && fosa.length > 0 && (
          <tr>
            <td colSpan={5} className="muted tiny" style={{ background: 'var(--surface-2)', textTransform: 'uppercase', letterSpacing: '.5px', padding: '6px 12px' }}>
              FOSA · Withdrawable savings
            </td>
          </tr>
        )}
        {(segmentEnabled ? fosa : bundle.deposits).map((d) => (
          <DepositRow key={d.account.id} d={d} counterpartyId={counterpartyId} currency={currency} segmentEnabled={segmentEnabled} />
        ))}

        {bundle.loans.map((row) => (
          <tr key={row.loan.id}>
            <td><strong>Loan</strong></td>
            <td>
              <div className="tiny-mono">{row.loan.loan_no}</div>
              <div className="muted tiny">{row.product_name} · {row.product_code}</div>
            </td>
            <td><StatusBadge status={row.loan.status} /></td>
            <td className="mono num" style={{ textAlign: 'right' }}>
              {currency} {fmtMoney(row.loan.principal_balance)}
              <div className="muted tiny">outstanding</div>
            </td>
            <td>
              <a className="btn btn-sm" href={`/loans/${row.loan.id}`}>Open →</a>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function DepositRow({ d, counterpartyId, currency, segmentEnabled }: {
  d: MemberDepositItem; counterpartyId: string; currency: string; segmentEnabled: boolean;
}) {
  return (
    <tr>
      <td>
        <strong>{d.product.segment === 'bosa' ? 'Deposit' : 'Savings'}</strong>
        {segmentEnabled && (
          <div style={{ marginTop: 2 }}>
            {d.product.segment === 'bosa'
              ? <Badge tone="warn">BOSA</Badge>
              : <Badge tone="neutral">FOSA</Badge>}
          </div>
        )}
      </td>
      <td>
        <div className="tiny-mono">{d.account.account_no}</div>
        <div className="muted tiny">{d.product.name} · {d.product.code}</div>
      </td>
      <td><StatusBadge status={d.account.status} /></td>
      <td className="mono num" style={{ textAlign: 'right' }}>
        {currency} {fmtMoney(d.account.current_balance)}
      </td>
      <td>
        <a className="btn btn-sm" href={`/deposits?member=${counterpartyId}&account=${d.account.id}`}>Open →</a>
      </td>
    </tr>
  );
}

function SummarySkeleton() {
  return (
    <table className="tbl" aria-hidden="true">
      <thead>
        <tr>
          <th>Type</th>
          <th>Account</th>
          <th>Status</th>
          <th style={{ textAlign: 'right' }}>Balance</th>
          <th style={{ width: 1 }}></th>
        </tr>
      </thead>
      <tbody>
        {Array.from({ length: 3 }).map((_, i) => (
          <tr key={i}>
            {Array.from({ length: 5 }).map((_, j) => (
              <td key={j}>
                <div style={{
                  background: 'var(--surface-2)',
                  height: 12,
                  borderRadius: 3,
                  opacity: 0.5,
                }} />
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
