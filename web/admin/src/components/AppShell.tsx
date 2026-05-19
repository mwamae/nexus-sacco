import type { ReactNode } from 'react';
import { useAuth } from '../auth/AuthContext';

export default function AppShell({ children }: { children: ReactNode }) {
  const { user, tenant, logout } = useAuth();
  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand">
          <span className="mk">N</span>
          <span>nexusSacco</span>
        </div>
        <span className="muted tiny">·</span>
        <span className="tiny mono">{tenant?.slug ?? 'platform'}</span>
        <div className="spacer" />
        <div className="who">
          <strong>{user?.full_name}</strong> · {user?.email}
        </div>
        <button className="btn" onClick={() => void logout()}>Sign out</button>
      </header>
      {children}
    </div>
  );
}
