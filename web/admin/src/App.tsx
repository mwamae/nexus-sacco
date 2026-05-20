import { AuthProvider, useAuth } from './auth/AuthContext';
import AppShell from './components/AppShell';
import { Tweaks } from './components/Tweaks';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import ResetPassword from './pages/ResetPassword';
import AcceptInvite from './pages/AcceptInvite';
import Roles from './pages/Roles';
import Users from './pages/Users';
import Members from './pages/Members';
import MemberOnboarding from './pages/MemberOnboarding';
import MemberProfile from './pages/MemberProfile';
import TenantOnboarding from './pages/TenantOnboarding';
import TenantProfile from './pages/TenantProfile';
import TenantSettings from './pages/TenantSettings';
import Organizations from './pages/Organizations';
import OrganizationOnboarding from './pages/OrganizationOnboarding';
import OrganizationProfile from './pages/OrganizationProfile';
import ApprovalsInbox from './pages/ApprovalsInbox';
import WorkflowDefinitions from './pages/WorkflowDefinitions';

function Gate() {
  const { status } = useAuth();
  const path = window.location.pathname;

  // Anonymous pages — live outside the auth gate.
  if (path === '/reset') return <ResetPassword />;
  if (path === '/invite/accept') return <AcceptInvite />;

  if (status === 'loading') {
    return (
      <div className="auth-shell">
        <div className="muted tiny">Loading…</div>
      </div>
    );
  }
  if (status === 'anonymous') return <Login />;

  let page;
  if (path === '/users') page = <Users />;
  else if (path === '/roles') page = <Roles />;
  else if (path === '/members/new') page = <MemberOnboarding />;
  else if (path === '/members') page = <Members />;
  else if (path.startsWith('/members/')) page = <MemberProfile />;
  else if (path === '/tenants/new') page = <TenantOnboarding />;
  else if (path.startsWith('/tenants/')) page = <TenantProfile />;
  else if (path === '/settings') page = <TenantSettings />;
  else if (path === '/orgs/new') page = <OrganizationOnboarding />;
  else if (path === '/orgs') page = <Organizations />;
  else if (path.startsWith('/orgs/')) page = <OrganizationProfile />;
  else if (path === '/approvals' || path.startsWith('/approvals/')) page = <ApprovalsInbox />;
  else if (path === '/workflows' || path.startsWith('/workflows/')) page = <WorkflowDefinitions />;
  else page = <Dashboard />;

  return <AppShell>{page}</AppShell>;
}

export default function App() {
  return (
    <AuthProvider>
      <Gate />
      <Tweaks />
    </AuthProvider>
  );
}
