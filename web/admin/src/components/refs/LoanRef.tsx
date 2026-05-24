// LoanRef — resolves loan_id → "L-2026-00003 · ProductCode" linked
// to /loans/<id>.

import { useEffect, useState } from 'react';
import { getLoan, type LoanDetail } from '../../api/client';
import { createCache } from './cache';

const cache = createCache<LoanDetail>('loan', getLoan);

export function LoanRef({
  loanId,
  fallback = '—',
}: {
  loanId?: string | null;
  fallback?: string;
}) {
  const [d, setD] = useState<LoanDetail | null>(null);
  useEffect(() => {
    if (!loanId) { setD(null); return; }
    let cancelled = false;
    cache.resolve(loanId).then((v) => { if (!cancelled) setD(v); });
    return () => { cancelled = true; };
  }, [loanId]);

  if (!loanId) return <span className="muted">{fallback}</span>;
  if (!d) {
    return (
      <span className="tiny-mono muted" title={`Resolving loan ${loanId}`}>
        {loanId.slice(0, 8)}…
      </span>
    );
  }
  const l = d.loan;
  return (
    <a className="tbl-link" href={`/loans/${l.id}`} title={`Loan ${l.id}`}>
      <span>{l.loan_no}</span>
    </a>
  );
}

export const __loanRefCache = cache;
