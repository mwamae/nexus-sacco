// Member 360 → Accounts → "Transactions" panel.
//
// Unified timeline of every cash movement a member has across their
// savings, loan, and share accounts. Cursor-paginated; 50 rows per
// page; "Load more" walks backwards in time.
//
// Replaces the old "MODULE PENDING — Single timeline of debits and
// credits. Pending the transactions ledger." placeholder in
// AccountsTab. Source data:
// GET /v1/member-ledger/{counterparty_id}
// — see services/savings/internal/store/member_ledger_store.go for
// the bucket semantics.

import { useCallback, useState } from 'react';
import {
  getMemberLedger,
  type LedgerPage,
  type LedgerRow,
  type LedgerSource,
} from '../api/client';
import { AsyncPanel, isTimeoutError } from './AsyncPanel';
import { Badge } from './Badge';

const PAGE_SIZE = 50;

// Human label + tone for the per-row type chip. Each enum value below
// is one of the source-specific txn_type strings the backend returns.
// Anything not in this map renders as a neutral chip with the raw
// type — that's the safety net for future enum additions.
type ChipDef = { label: string; tone: 'pos' | 'neg' | 'warn' | 'accent' | 'neutral' };

const TYPE_CHIPS: Record<string, ChipDef> = {
  // deposit_txn_type
  deposit:           { label: 'DEPOSIT',          tone: 'pos' },
  withdrawal:        { label: 'WITHDRAWAL',       tone: 'neg' },
  transfer_in:       { label: 'TRANSFER IN',      tone: 'pos' },
  transfer_out:      { label: 'TRANSFER OUT',     tone: 'neg' },
  interest_credit:   { label: 'INTEREST',         tone: 'pos' },
  opening_balance:   { label: 'OPENING BALANCE',  tone: 'neutral' },
  fee_debit:         { label: 'FEE',              tone: 'neg' },
  goal_payout:       { label: 'GOAL PAYOUT',      tone: 'neg' },
  // loan_txn_type
  disbursement:      { label: 'LOAN DISBURSEMENT', tone: 'pos' },
  repayment:         { label: 'LOAN REPAYMENT',    tone: 'neg' },
  fee_charge:        { label: 'LOAN FEE',          tone: 'warn' },
  interest_accrual:  { label: 'INTEREST ACCRUAL',  tone: 'neutral' },
  penalty_charge:    { label: 'PENALTY',           tone: 'warn' },
  penalty_waiver:    { label: 'PENALTY WAIVED',    tone: 'accent' },
  write_off:         { label: 'WRITE-OFF',         tone: 'neutral' },
  reversal:          { label: 'REVERSAL',          tone: 'warn' },
  // share_txn_type
  purchase:          { label: 'SHARE PURCHASE',   tone: 'neg' },
  redemption:        { label: 'SHARE REDEMPTION', tone: 'pos' },
  adjustment:        { label: 'SHARE ADJUSTMENT', tone: 'accent' },
  bonus_issue:       { label: 'BONUS SHARES',     tone: 'pos' },
};

function chipFor(t: string): ChipDef {
  return TYPE_CHIPS[t] ?? { label: t.replace(/_/g, ' ').toUpperCase(), tone: 'neutral' };
}

// Each source row links to its module page so officers can drill in
// for the full account view.
function moduleHrefFor(row: LedgerRow): string {
  switch (row.source) {
    case 'deposit': return `/deposits?account=${row.account_id}`;
    case 'loan':    return `/loans/${row.account_id}`;
    case 'share':   return `/shares?account=${row.account_id}`;
  }
}

function sourceLabel(s: LedgerSource): string {
  return s === 'deposit' ? 'Deposits' : s === 'loan' ? 'Loans' : 'Shares';
}

function fmtMoney(s: string): string {
  const n = parseFloat(s);
  if (!isFinite(n) || n === 0) return '—';
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

export function MemberLedgerPanel({ memberId, currency }: { memberId: string; currency: string }) {
  // The first page is loaded via AsyncPanel (uses our standard
  // skeleton + typed error). Subsequent pages are loaded via the
  // "Load more" button below, which calls the API directly and
  // appends to state.
  const fetcher = useCallback(
    () => getMemberLedger(memberId, { limit: PAGE_SIZE }),
    [memberId],
  );

  return (
    <div className="card" style={{ marginTop: 14 }}>
      <div className="card-hd">
        <h3>Transactions</h3>
        <span className="card-sub">
          Unified timeline across savings, loans, shares, and fees · newest first
        </span>
      </div>
      <div className="card-body flush">
        <AsyncPanel
          fetcher={fetcher}
          deps={[memberId]}
          isEmpty={(p) => p.rows.length === 0}
          empty={(
            <div className="empty" style={{ padding: 24, textAlign: 'center' }}>
              No transactions yet for this member.
            </div>
          )}
          errorTitle="Couldn't load transactions"
          errorMessage={(err) => isTimeoutError(err)
            ? "The savings service didn't respond in time. Try again."
            : "We couldn't fetch this member's transaction history."}
          skeleton={<LedgerSkeleton />}
        >
          {(firstPage) => (
            <LedgerView
              memberId={memberId}
              currency={currency}
              firstPage={firstPage}
            />
          )}
        </AsyncPanel>
      </div>
    </div>
  );
}

function LedgerView({
  memberId, currency, firstPage,
}: {
  memberId: string;
  currency: string;
  firstPage: LedgerPage;
}) {
  const [rows, setRows] = useState<LedgerRow[]>(firstPage.rows);
  const [cursor, setCursor] = useState<string | null>(firstPage.next_cursor ?? null);
  const [hasMore, setHasMore] = useState<boolean>(firstPage.has_more);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadErr, setLoadErr] = useState<string | null>(null);

  async function onLoadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setLoadErr(null);
    try {
      const next = await getMemberLedger(memberId, { limit: PAGE_SIZE, before: cursor });
      setRows((prev) => [...prev, ...next.rows]);
      setCursor(next.next_cursor ?? null);
      setHasMore(next.has_more);
    } catch (e) {
      setLoadErr(e instanceof Error ? e.message : 'Failed to load more.');
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <>
      <table className="tbl">
        <thead>
          <tr>
            <th>Date</th>
            <th>Type</th>
            <th>Account</th>
            <th style={{ textAlign: 'right' }}>Debit</th>
            <th style={{ textAlign: 'right' }}>Credit</th>
            <th style={{ textAlign: 'right' }} title="Source-account balance immediately after this row. Per-account — there is no single cross-module running total for a member.">
              Balance after
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const c = chipFor(r.txn_type);
            return (
              <tr key={`${r.source}-${r.txn_id}`}>
                <td className="tiny-mono" title={r.posted_at}>
                  {r.posted_at.slice(0, 10)}
                </td>
                <td>
                  <Badge tone={c.tone}>{c.label}</Badge>
                  {r.narration && (
                    <div className="muted tiny" style={{ marginTop: 2 }}>{r.narration}</div>
                  )}
                </td>
                <td className="tiny">
                  <a href={moduleHrefFor(r)} className="tbl-link tiny-mono">{r.account_label}</a>
                  <div className="muted tiny">{sourceLabel(r.source)}</div>
                </td>
                <td className="mono num" style={{ textAlign: 'right' }}>
                  {r.debit === '0' || r.debit === '0.00' ? <span className="muted">—</span> : `${currency} ${fmtMoney(r.debit)}`}
                </td>
                <td className="mono num" style={{ textAlign: 'right' }}>
                  {r.credit === '0' || r.credit === '0.00' ? <span className="muted">—</span> : `${currency} ${fmtMoney(r.credit)}`}
                </td>
                <td className="mono num" style={{ textAlign: 'right' }}>
                  {currency} {fmtMoney(r.balance_after)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <div style={{ padding: 12, borderTop: '1px solid var(--border)', display: 'flex', gap: 12, alignItems: 'center', justifyContent: 'center' }}>
        {hasMore ? (
          <>
            <button className="btn btn-sm" disabled={loadingMore} onClick={() => void onLoadMore()}>
              {loadingMore ? 'Loading more…' : 'Load more'}
            </button>
            <span className="muted tiny">{rows.length} loaded</span>
          </>
        ) : (
          <span className="muted tiny">All {rows.length} transactions loaded.</span>
        )}
      </div>
      {loadErr && (
        <div style={{ padding: 12 }}>
          <div className="alert alert-error">{loadErr}</div>
        </div>
      )}
    </>
  );
}

// Skeleton: 6 placeholder rows of the right shape. Better than a
// generic "Loading…" because the user sees the table appear in-situ.
function LedgerSkeleton() {
  return (
    <table className="tbl" aria-hidden="true">
      <thead>
        <tr>
          <th>Date</th>
          <th>Type</th>
          <th>Account</th>
          <th style={{ textAlign: 'right' }}>Debit</th>
          <th style={{ textAlign: 'right' }}>Credit</th>
          <th style={{ textAlign: 'right' }}>Balance after</th>
        </tr>
      </thead>
      <tbody>
        {Array.from({ length: 6 }).map((_, i) => (
          <tr key={i}>
            {Array.from({ length: 6 }).map((_, j) => (
              <td key={j}>
                <div style={{
                  background: 'var(--surface-2)',
                  height: 12,
                  borderRadius: 3,
                  opacity: 0.5 + (i % 2) * 0.1,
                }} />
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}
