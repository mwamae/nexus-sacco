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

  // More specific keys must come before the catch-all so vite-proxy
  // routes /api/v1/members* to the member service, not identity.
  return {
    plugins: [react()],
    server: {
      host: true,
      port: 5173,
      allowedHosts: [appDomain, `.${appDomain}`],
      proxy: {
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
        '/api': {
          target: identityTarget,
          changeOrigin: false,         // preserve Host so backend sees tenant subdomain
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
      },
    },
  };
});
