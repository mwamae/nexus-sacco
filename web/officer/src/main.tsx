import React from 'react';
import { createRoot } from 'react-dom/client';
import { registerSW } from 'virtual:pwa-register';
import App from './App';

// Register the service worker. autoUpdate is configured in vite.config —
// when a new SW takes control, the next nav serves the fresh app shell.
registerSW({ immediate: true });

const root = document.getElementById('root');
if (root) {
  createRoot(root).render(<React.StrictMode><App /></React.StrictMode>);
}
