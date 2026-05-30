import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// We want the dev server to be reachable on subdomains of the configured
// app domain (e.g. tujenge.nexussacco.local:5173). `host: true` binds
// to all interfaces; `allowedHosts` whitelists patterns. The Identity
// service runs on :8081; /api/* is proxied to it so the frontend just
// hits same-origin paths.

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const appDomain = env.VITE_APP_DOMAIN || 'nexussacco.local';
  const identityTarget = env.VITE_API_TARGET || 'http://localhost:8081';
  const memberTarget = env.VITE_MEMBER_TARGET || 'http://localhost:8082';
  const workflowTarget = env.VITE_WORKFLOW_TARGET || 'http://localhost:8083';
  const savingsTarget = env.VITE_SAVINGS_TARGET || 'http://localhost:8084';
  const notificationTarget = env.VITE_NOTIFICATION_TARGET || 'http://localhost:8085';
  const accountingTarget = env.VITE_ACCOUNTING_TARGET || 'http://localhost:8086';
  const mpesaTarget = env.VITE_MPESA_TARGET || 'http://localhost:8087';

  // More specific keys must come before the catch-all so vite-proxy
  // routes /api/v1/members* to the member service, not identity.
  return {
    plugins: [react()],
    server: {
      host: true,
      port: 5173,
      allowedHosts: [appDomain, `.${appDomain}`],
      proxy: {
        '/api/v1/applications': {
          target: memberTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/counterparties': {
          target: memberTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/members': {
          target: memberTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/orgs': {
          target: memberTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/workflows': {
          target: workflowTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/workflow-instances': {
          target: workflowTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/inbox-status': {
          target: workflowTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/share-accounts': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/virtual-tills': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/share-liens': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/share-policy': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/deposit-products': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/deposit-accounts': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/interest-runs': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/interest-run-lines': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/dividend-runs': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/dividend-run-lines': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-products': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-purpose-categories': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-applications': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-guarantees': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase 1.5a/b — collateral standalone endpoints
        // (/v1/collateral/{id}/*, /v1/loan-collateral/by-counterparty/{id}).
        '/api/v1/collateral': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-collateral': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase-1 follow-up — valuation report download.
        '/api/v1/collateral-valuations': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase-1 follow-up — Documents + Comments standalone endpoints
        // (/v1/loan-documents/{id}/*, /v1/loan-comments/{id}/*,
        // /v1/loan-comments/templates, /v1/loan-comments/search).
        '/api/v1/loan-documents': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-comments': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loans': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase 5 — public guarantor-consent flow (no auth). The
        // visible SMS link is /g/{token}; the SPA page calls these
        // API routes which the savings service exposes outside its
        // /v1 auth wing.
        '/api/p/guarantor-consent': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase 1.5b — public third-party pledger consent flow.
        '/api/p/pledger-consent': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Phase-1 follow-up — public loan-comments member reply route.
        '/api/p/comments': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-transactions': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/loan-reports': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/member-statements': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/member-ledger': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/provisioning': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/fiscal-years': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/bank-accounts': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/bank-statements': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/bank-statement-lines': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/tills': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // ─── Collection Desk endpoints (savings) ──────────────────────
        // Most specific first: GET /v1/till-sessions/current is the
        // Desk's "do I have an open till?" probe — owned by savings,
        // not accounting. The broader /v1/till-sessions catch-all
        // below routes to accounting for open/close/detail.
        '/api/v1/till-sessions/current': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/receipts': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/fees': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // GET /v1/counterparties/{id}/outstanding is a Desk endpoint
        // owned by savings — but the broader /v1/counterparties
        // namespace routes to member. Regex-keyed entry (prefix '^')
        // wins over the plain-prefix member entry below.
        '^/api/v1/counterparties/[^/]+/outstanding': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/till-sessions': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/cash-transfers': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/cash-position': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/fixed-assets': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/depreciation-runs': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/budgets': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/exports': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/notifications/stream': {
          // SSE — http-proxy pipes chunked responses by default which
          // is what we want; keep this entry above the catch-all
          // /api/v1/notifications so it wins specificity.
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/notifications': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/notification-events': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/notification-templates': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/notification-config': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/otp-settings': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/otp-requests': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/campaigns': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/campaign-settings': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/scheduled-jobs': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/credits': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/pdf-documents': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Platform-side notification endpoints — must be the SPECIFIC
        // subpaths the notification service owns, NOT a catch-all on
        // /api/v1/platform (the identity service owns the rest, e.g.
        // /v1/platform/tenants).
        '/api/v1/platform/credits': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/platform/notification-config': {
          target: notificationTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Accounting service — Stage 11.
        '/api/v1/coa': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/journal-entries': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/periods': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // More-specific first: fees-summary lives on savings (where
        // receipts + fee_catalog are owned). The matching XLSX export
        // at /api/v1/exports/fees-summary.xlsx is on accounting (see
        // exports.go::buildFeesSummary) so downloadReport() still
        // works via the existing accounting proxy below.
        '/api/v1/reports/fees-summary': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/reports': {
          target: accountingTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/pending-approvals': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/approval-settings': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/collection-cases': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/promises': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/wht-schedule': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/wht-certificate': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/mpesa': {
          target: mpesaTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // /v1/finance/* lives on savings — the posting-outbox
        // viewer + the /v1/finance/health alias the System Health
        // page polls. Without this entry the calls fall through
        // to the catchall /api → identity proxy and 404.
        '/api/v1/finance': {
          target: savingsTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        // Daraja-facing webhook routes — Safaricom refuses URLs that
        // contain "mpesa", so the C2B + B2C webhook endpoints live at
        // /v1/c2b/* and /v1/b2c/* with no /mpesa segment. Proxy them
        // through to the mpesa service so dev calls (curl, sandbox
        // simulator) reach the right backend.
        '/api/v1/c2b': {
          target: mpesaTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api/v1/b2c': {
          target: mpesaTarget,
          changeOrigin: false,
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
        '/api': {
          target: identityTarget,
          changeOrigin: false,         // preserve Host so backend sees tenant subdomain
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
      },
    },
  };
});
