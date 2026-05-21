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
import Shares from './pages/Shares';
import Deposits from './pages/Deposits';
import DepositProducts from './pages/DepositProducts';
import InterestRunsPage from './pages/InterestRuns';
import DividendRunsPage from './pages/DividendRuns';
import LoanProductsPage from './pages/LoanProducts';
import LoansPage from './pages/Loans';
import CollectionsPage from './pages/Collections';
import LoanReportsPage from './pages/LoanReports';
import CashApprovalsPage from './pages/CashApprovals';
import NotificationsPage from './pages/Notifications';

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
  else if (path === '/shares' || path.startsWith('/shares/')) page = <Shares />;
  else if (path === '/deposit-products' || path.startsWith('/deposit-products/')) page = <DepositProducts />;
  else if (path === '/deposits' || path.startsWith('/deposits/')) page = <Deposits />;
  else if (path === '/interest-runs' || path.startsWith('/interest-runs/')) page = <InterestRunsPage />;
  else if (path === '/dividend-runs' || path.startsWith('/dividend-runs/')) page = <DividendRunsPage />;
  else if (path === '/loan-products' || path.startsWith('/loan-products/')) page = <LoanProductsPage />;
  else if (path === '/loans' || path.startsWith('/loans/')) page = <LoansPage />;
  else if (path === '/collections' || path.startsWith('/collections/')) page = <CollectionsPage />;
  else if (path === '/loan-reports' || path.startsWith('/loan-reports/')) page = <LoanReportsPage />;
  else if (path === '/cash-approvals' || path.startsWith('/cash-approvals/')) page = <CashApprovalsPage />;
  else if (path === '/notifications' || path.startsWith('/notifications/')) page = <NotificationsPage />;
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
