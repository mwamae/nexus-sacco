// Member-portal app shell. Hash-based routing keeps this file simple
// — no react-router needed for the four pages this PR ships.

import { useEffect, useState } from 'react';
import { hasSession, memberLogout } from './api';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import Calculator from './pages/Calculator';

type Route = 'login' | 'dashboard' | 'calculator';

function currentRoute(): Route {
  const hash = window.location.hash.replace('#/', '');
  if (hash === 'calculator') return 'calculator';
  if (hash === 'dashboard' && hasSession()) return 'dashboard';
  return hasSession() ? 'dashboard' : 'login';
}

export default function App() {
  const [route, setRoute] = useState<Route>(currentRoute());
  useEffect(() => {
    const handler = () => setRoute(currentRoute());
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);

  return (
    <div className="container">
      <header style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <h1 style={{ margin: 0 }}>nexusSacco · Member portal</h1>
        {hasSession() && (
          <nav style={{ display: 'flex', gap: 12 }}>
            <a href="#/dashboard">Dashboard</a>
            <a href="#/calculator">Calculator</a>
            <a onClick={memberLogout} style={{ cursor: 'pointer', color: '#c33' }}>Sign out</a>
          </nav>
        )}
      </header>
      {route === 'login' && <Login onLoggedIn={() => { window.location.hash = '#/dashboard'; }} />}
      {route === 'dashboard' && <Dashboard />}
      {route === 'calculator' && <Calculator />}
    </div>
  );
}
