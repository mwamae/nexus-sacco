// Route-name registry — the canonical place to look up a breadcrumb
// trail for any URL the app routes to. Used by the AppShell topbar so
// that the page header never falls back to rendering the raw URL path.
//
// Add a new entry here when you add a new route in App.tsx. Static
// routes get an exact `path` + the full breadcrumb trail; dynamic
// routes get a `match` predicate and a `baseTrail` that pages can
// suffix at runtime via usePageCrumb() (e.g. "Members → Jane Doe").

export type RouteTrail = string[];

type StaticRoute = {
  kind: 'static';
  path: string;
  trail: RouteTrail;
};

type DynamicRoute = {
  kind: 'dynamic';
  // Distinguishing prefix so multiple dynamic routes don't conflict.
  // Order in the array matters — more-specific patterns first.
  match: (path: string) => boolean;
  baseTrail: RouteTrail;
  // Rendered after baseTrail when the page hasn't set a dynamic
  // suffix via usePageCrumb(). Keep generic ("Profile", "Details").
  fallbackSuffix?: string;
};

// Order matters: the FIRST matching route wins. Put more-specific
// dynamic patterns above their broader siblings (e.g. /members/:id/
// statement before /members/:id).
export const ROUTES: ReadonlyArray<StaticRoute | DynamicRoute> = [
  // ─── Top level ───
  { kind: 'static', path: '/',                              trail: ['Home'] },

  // ─── Servicing ───
  // Membership / member-onboarding queue (the people-applying-to-join
  // surface). Loan applications live under /loans and have their own
  // crumb trail — see the Lending block below.
  { kind: 'static', path: '/applications',                  trail: ['Member onboarding'] },
  { kind: 'static', path: '/applications/new',              trail: ['Member onboarding', 'New'] },
  { kind: 'dynamic', match: (p) => /^\/applications\/[^/]+/.test(p),
    baseTrail: ['Member onboarding'], fallbackSuffix: 'Application' },

  { kind: 'static', path: '/members',                       trail: ['Members'] },
  // /members/new redirects to /applications/new (Phase C deletion of
  // the legacy onboarding wizard). Kept here so the redirect screen's
  // breadcrumb shows something meaningful for the split-second it's up.
  { kind: 'static', path: '/members/new',                   trail: ['Members', 'New'] },
  { kind: 'dynamic', match: (p) => /^\/members\/[^/]+\/statement$/.test(p),
    baseTrail: ['Members'], fallbackSuffix: 'Statement' },
  { kind: 'dynamic', match: (p) => /^\/members\/[^/]+/.test(p),
    baseTrail: ['Members'], fallbackSuffix: 'Profile' },

  { kind: 'static', path: '/orgs',                          trail: ['Organisations'] },
  { kind: 'static', path: '/orgs/new',                      trail: ['Organisations', 'Onboarding'] },
  { kind: 'dynamic', match: (p) => /^\/orgs\/[^/]+/.test(p),
    baseTrail: ['Organisations'], fallbackSuffix: 'Profile' },

  { kind: 'static', path: '/shares',                        trail: ['Shares'] },
  { kind: 'dynamic', match: (p) => /^\/shares\//.test(p),    baseTrail: ['Shares'],            fallbackSuffix: 'Detail' },

  { kind: 'static', path: '/deposits',                      trail: ['Deposits'] },
  { kind: 'dynamic', match: (p) => /^\/deposits\//.test(p),  baseTrail: ['Deposits'],          fallbackSuffix: 'Detail' },

  // Loans Phase 1 — consolidated section. Order matters: the more-
  // specific /loans/* static + dynamic patterns must come BEFORE the
  // dynamic /loans/(:id) fallback at the end of the block.
  { kind: 'static', path: '/loans',                         trail: ['Loans', 'Dashboard'] },
  { kind: 'static', path: '/loans/applications',            trail: ['Loans', 'Applications'] },
  { kind: 'static', path: '/loans/applications/new',        trail: ['Loans', 'Applications', 'New'] },
  { kind: 'dynamic', match: (p) => /^\/loans\/applications\/[^/]+/.test(p),
    baseTrail: ['Loans', 'Applications'], fallbackSuffix: 'Application' },
  { kind: 'static', path: '/loans/register',                trail: ['Loans', 'Register'] },
  { kind: 'dynamic', match: (p) => /^\/loans\/register\/[^/]+/.test(p),
    baseTrail: ['Loans', 'Register'], fallbackSuffix: 'Loan' },
  { kind: 'static', path: '/loans/collections',             trail: ['Loans', 'Collections'] },
  { kind: 'static', path: '/loans/reports',                 trail: ['Loans', 'Reports'] },
  { kind: 'static', path: '/loans/provisioning',            trail: ['Loans', 'Provisioning'] },
  { kind: 'static', path: '/loans/products',                trail: ['Loans', 'Products'] },
  // Legacy register page (kept one release) — its old breadcrumb stays.
  { kind: 'static', path: '/loans/legacy',                  trail: ['Lending', 'Loans (legacy)'] },
  { kind: 'dynamic', match: (p) => /^\/loans\/legacy\//.test(p), baseTrail: ['Lending', 'Loans (legacy)'], fallbackSuffix: 'Loan' },

  { kind: 'static', path: '/collections',                   trail: ['Lending', 'Collections'] },
  { kind: 'dynamic', match: (p) => /^\/collections\//.test(p),
    baseTrail: ['Lending', 'Collections'], fallbackSuffix: 'Case' },

  { kind: 'static', path: '/loan-reports',                  trail: ['Lending', 'Loan reports'] },
  { kind: 'dynamic', match: (p) => /^\/loan-reports\//.test(p),
    baseTrail: ['Lending', 'Loan reports'], fallbackSuffix: 'Report' },

  { kind: 'static', path: '/provisioning',                  trail: ['Lending', 'Provisioning'] },
  { kind: 'dynamic', match: (p) => /^\/provisioning\//.test(p),
    baseTrail: ['Lending', 'Provisioning'], fallbackSuffix: 'Run' },

  { kind: 'static', path: '/cash-approvals',                trail: ['Approvals', 'Cash'] },
  { kind: 'dynamic', match: (p) => /^\/cash-approvals\//.test(p),
    baseTrail: ['Approvals', 'Cash'], fallbackSuffix: 'Approval' },

  { kind: 'static', path: '/interest-runs',                 trail: ['Servicing', 'Interest runs'] },
  { kind: 'dynamic', match: (p) => /^\/interest-runs\//.test(p),
    baseTrail: ['Servicing', 'Interest runs'], fallbackSuffix: 'Run' },

  { kind: 'static', path: '/dividend-runs',                 trail: ['Servicing', 'Dividend runs'] },
  { kind: 'dynamic', match: (p) => /^\/dividend-runs\//.test(p),
    baseTrail: ['Servicing', 'Dividend runs'], fallbackSuffix: 'Run' },

  // ─── Approvals (workflow inbox + definitions) ───
  { kind: 'static', path: '/approvals',                     trail: ['Approvals', 'Inbox'] },
  { kind: 'dynamic', match: (p) => /^\/approvals\//.test(p),
    baseTrail: ['Approvals', 'Inbox'], fallbackSuffix: 'Item' },

  { kind: 'static', path: '/workflows',                     trail: ['Approvals', 'Definitions'] },
  { kind: 'dynamic', match: (p) => /^\/workflows\//.test(p),
    baseTrail: ['Approvals', 'Definitions'], fallbackSuffix: 'Workflow' },

  // ─── Finance / accounting ───
  { kind: 'static', path: '/accounting/dashboard',          trail: ['Finance', 'Dashboard'] },
  { kind: 'static', path: '/accounting/chart-of-accounts',  trail: ['Finance', 'Chart of Accounts'] },
  { kind: 'static', path: '/accounting/journal-entries',    trail: ['Finance', 'Journal Entries'] },
  { kind: 'dynamic', match: (p) => /^\/accounting\/journal-entries\//.test(p),
    baseTrail: ['Finance', 'Journal Entries'], fallbackSuffix: 'Entry' },
  { kind: 'static', path: '/accounting/trial-balance',      trail: ['Finance', 'Trial Balance'] },
  { kind: 'static', path: '/accounting/balance-sheet',      trail: ['Finance', 'Balance Sheet'] },
  { kind: 'static', path: '/accounting/income-statement',   trail: ['Finance', 'Income Statement'] },
  { kind: 'static', path: '/accounting/changes-in-equity',  trail: ['Finance', 'Changes in Equity'] },
  { kind: 'static', path: '/accounting/cash-flow',          trail: ['Finance', 'Cash Flow'] },
  { kind: 'static', path: '/accounting/year-end-close',     trail: ['Finance', 'Year-end close'] },
  { kind: 'static', path: '/accounting/sasra-return',       trail: ['Finance', 'SASRA return'] },

  { kind: 'static', path: '/bank-accounts',                 trail: ['Finance', 'Bank reconciliation'] },
  { kind: 'dynamic', match: (p) => /^\/bank-accounts\//.test(p),
    baseTrail: ['Finance', 'Bank reconciliation'], fallbackSuffix: 'Account' },

  { kind: 'static', path: '/cash-management',               trail: ['Finance', 'Cash & float'] },

  { kind: 'static', path: '/fixed-assets',                  trail: ['Finance', 'Fixed assets'] },
  { kind: 'dynamic', match: (p) => /^\/fixed-assets\//.test(p),
    baseTrail: ['Finance', 'Fixed assets'], fallbackSuffix: 'Asset' },

  { kind: 'static', path: '/budgets',                       trail: ['Finance', 'Budgets & variance'] },
  { kind: 'dynamic', match: (p) => /^\/budgets\/[^/]+\/variance$/.test(p),
    baseTrail: ['Finance', 'Budgets & variance'], fallbackSuffix: 'Variance' },
  { kind: 'dynamic', match: (p) => /^\/budgets\//.test(p),
    baseTrail: ['Finance', 'Budgets & variance'], fallbackSuffix: 'Budget' },

  // ─── Engagement ───
  { kind: 'static', path: '/notifications',                 trail: ['Engagement', 'Notifications'] },
  { kind: 'dynamic', match: (p) => /^\/notifications\//.test(p),
    baseTrail: ['Engagement', 'Notifications'], fallbackSuffix: 'Notification' },

  { kind: 'static', path: '/credits',                       trail: ['Engagement', 'Credits'] },
  { kind: 'dynamic', match: (p) => /^\/credits\//.test(p),
    baseTrail: ['Engagement', 'Credits'], fallbackSuffix: 'Detail' },

  { kind: 'static', path: '/campaigns',                     trail: ['Engagement', 'Campaigns'] },
  { kind: 'dynamic', match: (p) => /^\/campaigns\//.test(p),
    baseTrail: ['Engagement', 'Campaigns'], fallbackSuffix: 'Campaign' },

  { kind: 'static', path: '/notification-templates',        trail: ['Engagement', 'Templates'] },
  { kind: 'dynamic', match: (p) => /^\/notification-templates\//.test(p),
    baseTrail: ['Engagement', 'Templates'], fallbackSuffix: 'Template' },

  { kind: 'static', path: '/scheduled-jobs',                trail: ['Engagement', 'Scheduled jobs'] },
  { kind: 'dynamic', match: (p) => /^\/scheduled-jobs\//.test(p),
    baseTrail: ['Engagement', 'Scheduled jobs'], fallbackSuffix: 'Job' },

  // ─── Administration ───
  { kind: 'static', path: '/users',                         trail: ['Administration', 'Staff'] },
  { kind: 'static', path: '/roles',                         trail: ['Administration', 'Roles & permissions'] },
  { kind: 'static', path: '/settings',                      trail: ['Administration', 'Settings'] },
  { kind: 'static', path: '/settings/mpesa',                trail: ['Administration', 'Settings', 'M-PESA paybills'] },
  { kind: 'static', path: '/settings/loans-policy',         trail: ['Administration', 'Settings', 'Loans policy'] },

  { kind: 'static', path: '/deposit-products',              trail: ['Administration', 'Deposit products'] },
  { kind: 'dynamic', match: (p) => /^\/deposit-products\//.test(p),
    baseTrail: ['Administration', 'Deposit products'], fallbackSuffix: 'Product' },

  { kind: 'static', path: '/loan-products',                 trail: ['Administration', 'Loan products'] },
  { kind: 'dynamic', match: (p) => /^\/loan-products\//.test(p),
    baseTrail: ['Administration', 'Loan products'], fallbackSuffix: 'Product' },

  // ─── Platform (apex host only) ───
  { kind: 'static', path: '/platform/system-health',        trail: ['Platform', 'System health'] },
  { kind: 'static', path: '/platform/credits',              trail: ['Platform', 'Credits'] },
  { kind: 'dynamic', match: (p) => /^\/platform\/credits\//.test(p),
    baseTrail: ['Platform', 'Credits'], fallbackSuffix: 'Detail' },

  { kind: 'static', path: '/tenants/new',                   trail: ['Platform', 'New tenant'] },
  { kind: 'dynamic', match: (p) => /^\/tenants\//.test(p),
    baseTrail: ['Platform'], fallbackSuffix: 'Tenant profile' },
];

// trailFor resolves a path (+ optional dynamic suffix from the active
// page) into the segments shown after the tenant label. NEVER returns
// the raw path — unknown routes fall back to a humanised slug derived
// from the last path segment.
export function trailFor(path: string, dynamicSuffix?: string | null): RouteTrail {
  for (const r of ROUTES) {
    if (r.kind === 'static') {
      if (r.path === path) return r.trail;
    } else if (r.match(path)) {
      const suffix = (dynamicSuffix ?? '').trim() || r.fallbackSuffix || '';
      return suffix ? [...r.baseTrail, suffix] : [...r.baseTrail];
    }
  }
  // Last-ditch defence — a route that was added in App.tsx but forgotten
  // here. Humanise the last segment ("loan-reports" → "Loan reports")
  // rather than leak "/loan-reports".
  const seg = path.split('/').filter(Boolean).pop() ?? '';
  return [humanizeSegment(seg) || 'Page'];
}

export function humanizeSegment(s: string): string {
  if (!s) return '';
  return s
    .replace(/[-_]+/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase());
}
