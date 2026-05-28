// /cash-approvals — REDIRECT-ONLY surface.
//
// The legacy cash-approvals queue was retired in the unified-
// approvals migration; every cash kind now routes through the
// workflow engine and surfaces in /approvals. We keep this route
// for one release as a redirect so bookmarks + external links keep
// working — the next major can delete the file entirely (App.tsx
// route + this file).
//
// We forward to the Approvals Inbox with kinds= preset to the full
// cash family so the inbox lands filtered to what a user previously
// saw on this page. window.location.replace preserves the back-
// button (the user can navigate away normally rather than re-trigger
// the redirect on every back-press).

import { useEffect } from 'react';

const REDIRECT_TARGET =
  '/approvals?kinds=cash_deposit,cash_withdrawal,cash_account_transfer,' +
  'share_purchase,share_transfer,share_bonus_issue,share_lien,' +
  'loan_disbursement,loan_repayment,loan_settle,loan_reverse,' +
  'loan_write_off,loan_reschedule,loan_moratorium,loan_settlement_discount,' +
  'fee_posting,welfare_posting,application_fee,member_bosa_exit';

export default function CashApprovals() {
  useEffect(() => {
    window.location.replace(REDIRECT_TARGET);
  }, []);

  return (
    <div className="page">
      <div className="empty">
        Redirecting to the unified Approvals Inbox…
        <div className="muted tiny" style={{ marginTop: 8 }}>
          The cash approvals queue moved to <code>/approvals</code> in the
          unified-approvals migration. Update any bookmarks pointing here.
        </div>
      </div>
    </div>
  );
}
