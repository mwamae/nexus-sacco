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

import { useCallback } from 'react';
import {
  getDepositAccountsByMember,
  getMemberLoanHistory,
  getShareAccountByMember,
  type Loan,
  type MemberDepositItem,
  type ShareAccountView,
} from '../api/client';
import { AsyncPanel, isTimeoutError } from './AsyncPanel';
import { StatusBadge } from './Badge';

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
          {(b) => <SummaryTable bundle={b} counterpartyId={counterpartyId} currency={currency} />}
        </AsyncPanel>
      </div>
    </div>
  );
}

function SummaryTable({
  bundle,
  counterpartyId,
  currency,
}: {
  bundle: Bundle;
  counterpartyId: string;
  currency: string;
}) {
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

        {bundle.deposits.map((d) => (
          <tr key={d.account.id}>
            <td><strong>Savings</strong></td>
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
