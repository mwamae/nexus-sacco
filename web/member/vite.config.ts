import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Member self-service portal — separate app from the staff admin SPA.
// Proxies /api/* to the savings + identity services in dev; in prod
// the gateway/edge router handles the same prefix-based routing.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5174,
    proxy: {
      '/api': {
        target: 'http://localhost:8084',
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/api/, ''),
      },
    },
  },
  build: {
    outDir: 'dist',
    target: 'es2022',
  },
});
