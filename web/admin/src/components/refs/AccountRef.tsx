// AccountRef — kind-aware account resolver.
//
//   kindHint = 'savings_deposit' | 'deposit' → getDepositAccount(id)
//                renders "DPA-… · ProductName" → /deposits?account=id
//   kindHint = 'share_purchase' | 'share'    → getShareAccount(id)
//                renders "SHA-… · Shares"    → /shares?account=id
//   kindHint = 'loan_repayment' | 'loan'     → getLoan(id) (LoanRef)
//                renders "L-2026-00003 · Normal Loan" → /loans/id
//   other kinds (fee, welfare)               → fallback to fallbackText
//                or the raw id (since fee lines reuse their own id as
//                account_id with no real account).
//
// Each underlying resolver uses its own module cache; concurrent
// lookups in a list dedupe to one fetch per distinct id.

import { useEffect, useState } from 'react';
import {
  getDepositAccount,
  getShareAccount,
  type DepositAccountView,
  type ShareAccount,
} from '../../api/client';
import { LoanRef } from './LoanRef';
import { createCache } from './cache';

const depositCache = createCache<DepositAccountView>('deposit_account', getDepositAccount);
const shareCache   = createCache<ShareAccount>('share_account', getShareAccount);

type Kind = 'savings_deposit' | 'deposit' | 'share_purchase' | 'share' | 'loan_repayment' | 'loan' | string;

export function AccountRef({
  accountId,
  kindHint,
  fallbackText = '—',
}: {
  accountId?: string | null;
  kindHint?: Kind;
  fallbackText?: string;
}) {
  if (!accountId) return <span className="muted">{fallbackText}</span>;

  // Loans get the dedicated LoanRef (which has its own cache + link).
  if (kindHint === 'loan' || kindHint === 'loan_repayment') {
    return <LoanRef loanId={accountId} fallback={fallbackText} />;
  }
  if (kindHint === 'share' || kindHint === 'share_purchase') {
    return <ShareAccountInner accountId={accountId} fallbackText={fallbackText} />;
  }
  if (kindHint === 'deposit' || kindHint === 'savings_deposit') {
    return <DepositAccountInner accountId={accountId} fallbackText={fallbackText} />;
  }
  // Unknown kind → degraded slice display rather than guessing.
  return (
    <span className="tiny-mono muted" title={`Unknown account kind for ${accountId}`}>
      {accountId.slice(0, 8)}…
    </span>
  );
}

function DepositAccountInner({ accountId, fallbackText }: { accountId: string; fallbackText: string }) {
  const [v, setV] = useState<DepositAccountView | null>(null);
  useEffect(() => {
    let c = false;
    depositCache.resolve(accountId).then((x) => { if (!c) setV(x); });
    return () => { c = true; };
  }, [accountId]);
  if (!v) return <SliceFallback id={accountId} kind="deposit account" fallback={fallbackText} />;
  return (
    <a className="tbl-link" href={`/deposits?account=${v.account.id}`} title={`Account ${v.account.id}`}>
      <span>{v.account.account_no}</span>
      {v.product?.name && (
        <span className="muted" style={{ marginLeft: 6, fontSize: '90%' }}>· {v.product.name}</span>
      )}
    </a>
  );
}

function ShareAccountInner({ accountId, fallbackText }: { accountId: string; fallbackText: string }) {
  const [a, setA] = useState<ShareAccount | null>(null);
  useEffect(() => {
    let c = false;
    shareCache.resolve(accountId).then((x) => { if (!c) setA(x); });
    return () => { c = true; };
  }, [accountId]);
  if (!a) return <SliceFallback id={accountId} kind="share account" fallback={fallbackText} />;
  return (
    <a className="tbl-link" href={`/shares?account=${a.id}`} title={`Share account ${a.id}`}>
      <span>{a.account_no}</span>
      <span className="muted" style={{ marginLeft: 6, fontSize: '90%' }}>· Shares</span>
    </a>
  );
}

function SliceFallback({ id, kind, fallback }: { id: string; kind: string; fallback: string }) {
  if (!id) return <span className="muted">{fallback}</span>;
  return (
    <span className="tiny-mono muted" title={`Resolving ${kind} ${id}`}>
      {id.slice(0, 8)}…
    </span>
  );
}

export const __accountRefCaches = { depositCache, shareCache };
