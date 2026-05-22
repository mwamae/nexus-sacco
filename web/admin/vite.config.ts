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
        '/api/v1/share-accounts': {
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
        '/api/v1/loans': {
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
        '/api': {
          target: identityTarget,
          changeOrigin: false,         // preserve Host so backend sees tenant subdomain
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
      },
    },
  };
});
