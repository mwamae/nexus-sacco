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
  const apiTarget = env.VITE_API_TARGET || 'http://localhost:8081';

  return {
    plugins: [react()],
    server: {
      host: true,
      port: 5173,
      allowedHosts: [appDomain, `.${appDomain}`],
      proxy: {
        '/api': {
          target: apiTarget,
          changeOrigin: false,         // preserve Host so backend sees tenant subdomain
          rewrite: (path) => path.replace(/^\/api/, ''),
        },
      },
    },
  };
});
