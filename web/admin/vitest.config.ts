import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    // happy-dom over jsdom: vitest 4's jsdom env skips wiring
    // window.localStorage onto globalThis when Node 22's experimental
    // localStorage is present, leaving our auth client's storage reads
    // undefined. happy-dom has no such clash.
    environment: 'happy-dom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    css: false,
  },
});
