import { AuthProvider, useAuth } from './auth/AuthContext';
import AppShell from './components/AppShell';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import ResetPassword from './pages/ResetPassword';

function Gate() {
  const { status } = useAuth();
  // Password reset is anonymous and lives outside the normal auth gate.
  if (window.location.pathname === '/reset') {
    return <ResetPassword />;
  }
  if (status === 'loading') {
    return (
      <div className="auth-shell">
        <div className="muted tiny">Loading…</div>
      </div>
    );
  }
  if (status === 'anonymous') return <Login />;
  return (
    <AppShell>
      <Dashboard />
    </AppShell>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <Gate />
    </AuthProvider>
  );
}
