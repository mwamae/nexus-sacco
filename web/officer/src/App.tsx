// Officer PWA shell. Mobile-first layout. Hash routing.

import { useEffect, useState } from 'react';
import { hasSession, officerLogout } from './api';
import Login from './pages/Login';
import Queue from './pages/Queue';

type Route = 'login' | 'queue';

function currentRoute(): Route {
  if (!hasSession()) return 'login';
  return 'queue';
}

export default function App() {
  const [route, setRoute] = useState<Route>(currentRoute());
  useEffect(() => {
    const handler = () => setRoute(currentRoute());
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);

  return (
    <div>
      <div className="topbar">
        <strong>nexusOfficer</strong>
        {hasSession() && <a onClick={officerLogout} style={{ cursor: 'pointer' }}>Sign out</a>}
      </div>
      <div className="container">
        {route === 'login' && <Login onLoggedIn={() => setRoute('queue')} />}
        {route === 'queue' && <Queue />}
      </div>
    </div>
  );
}
