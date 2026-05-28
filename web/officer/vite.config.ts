import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

// Officer field-PWA — installable on phone home screen. Offline-first
// behaviour (IndexedDB queue + background sync) is a follow-up; for
// this scaffold we register the service worker and ship the manifest
// so the app is installable, but actions still require connectivity.
export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'nexusSacco · Officer',
        short_name: 'nexusOfficer',
        description: 'Field collections + visits for SACCO loan officers',
        theme_color: '#2c5282',
        background_color: '#ffffff',
        display: 'standalone',
        start_url: '/',
        icons: [
          // Placeholder icons — replace with real PNGs at deployment time.
          { src: '/icon-192.png', sizes: '192x192', type: 'image/png' },
          { src: '/icon-512.png', sizes: '512x512', type: 'image/png' },
        ],
      },
      workbox: {
        // Cache the app shell; runtime API caching disabled until
        // the offline sync flow is wired (otherwise stale data risks).
        navigateFallback: '/index.html',
        runtimeCaching: [],
      },
    }),
  ],
  server: {
    port: 5175,
    proxy: {
      '/api': { target: 'http://localhost:8084', changeOrigin: true, rewrite: (p) => p.replace(/^\/api/, '') },
    },
  },
  build: { outDir: 'dist', target: 'es2022' },
});
