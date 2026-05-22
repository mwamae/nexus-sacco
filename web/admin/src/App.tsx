import { AuthProvider, useAuth } from './auth/AuthContext';
import AppShell from './components/AppShell';
import { ErrorBoundary } from './components/ErrorBoundary';
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
import ApplicationsQueuePage from './pages/Applications/ApplicationsQueue';
import NewApplicationPage from './pages/Applications/NewApplication';
import ApplicationDetailPage from './pages/Applications/ApplicationDetail';
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
import CampaignsPage from './pages/Campaigns';
import ScheduledJobsPage from './pages/ScheduledJobs';
import NotificationTemplatesPage from './pages/NotificationTemplates';
import CreditsPage from './pages/Credits';
import PlatformCreditsPage from './pages/PlatformCredits';
import ChartOfAccountsPage from './pages/Accounting/ChartOfAccounts';
import JournalEntriesPage from './pages/Accounting/JournalEntries';
import TrialBalancePage from './pages/Accounting/TrialBalance';
import BalanceSheetPage from './pages/Accounting/BalanceSheet';
import IncomeStatementPage from './pages/Accounting/IncomeStatement';
import ChangesInEquityPage from './pages/Accounting/ChangesInEquity';
import CashFlowPage from './pages/Accounting/CashFlow';
import FiscalYearClosePage from './pages/Accounting/FiscalYearClose';
import BankAccountsPage from './pages/Accounting/BankAccounts';
import BankAccountDetailPage from './pages/Accounting/BankAccountDetail';
import CashManagementPage from './pages/Accounting/CashManagement';
import FixedAssetsPage from './pages/Accounting/FixedAssets';
import BudgetsPage from './pages/Accounting/Budgets';
import BudgetDetailPage from './pages/Accounting/BudgetDetail';
import BudgetVariancePage from './pages/Accounting/BudgetVariance';
import SASRAReturnPage from './pages/Accounting/SASRAReturn';
import FinanceDashboardPage from './pages/Accounting/FinanceDashboard';
import MemberStatementPage from './pages/Members/MemberStatement';
import ProvisioningPage from './pages/Loans/Provisioning';

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
  else if (path === '/applications') page = <ApplicationsQueuePage />;
  else if (path === '/applications/new') page = <NewApplicationPage />;
  else if (path.startsWith('/applications/')) page = <ApplicationDetailPage />;
  else if (path === '/members/new') page = <MemberOnboarding />;
  else if (path === '/members') page = <Members />;
  else if (path.match(/^\/members\/[^/]+\/statement$/)) page = <MemberStatementPage />;
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
  else if (path === '/notifications' || path.startsWith('/notifications/')) {
    page = (
      <ErrorBoundary
        fallback={(_err, retry) => (
          <div className="page">
            <div className="page-hd"><h1>Notifications</h1></div>
            <div className="alert alert-error" style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
              <span>Couldn't load notifications.</span>
              <button className="btn btn-sm" onClick={retry}>Retry</button>
            </div>
          </div>
        )}
      >
        <NotificationsPage />
      </ErrorBoundary>
    );
  }
  else if (path === '/campaigns' || path.startsWith('/campaigns/')) page = <CampaignsPage />;
  else if (path === '/scheduled-jobs' || path.startsWith('/scheduled-jobs/')) page = <ScheduledJobsPage />;
  else if (path === '/notification-templates' || path.startsWith('/notification-templates/')) page = <NotificationTemplatesPage />;
  else if (path === '/credits' || path.startsWith('/credits/')) page = <CreditsPage />;
  else if (path === '/platform/credits' || path.startsWith('/platform/credits/')) page = <PlatformCreditsPage />;
  else if (path === '/accounting/chart-of-accounts') page = <ChartOfAccountsPage />;
  else if (path === '/accounting/journal-entries' || path.startsWith('/accounting/journal-entries/')) page = <JournalEntriesPage />;
  else if (path === '/accounting/trial-balance') page = <TrialBalancePage />;
  else if (path === '/accounting/balance-sheet') page = <BalanceSheetPage />;
  else if (path === '/accounting/income-statement') page = <IncomeStatementPage />;
  else if (path === '/accounting/changes-in-equity') page = <ChangesInEquityPage />;
  else if (path === '/accounting/cash-flow') page = <CashFlowPage />;
  else if (path === '/accounting/year-end-close') page = <FiscalYearClosePage />;
  else if (path === '/bank-accounts') page = <BankAccountsPage />;
  else if (path.startsWith('/bank-accounts/')) page = <BankAccountDetailPage />;
  else if (path === '/cash-management') page = <CashManagementPage />;
  else if (path === '/fixed-assets' || path.startsWith('/fixed-assets/')) page = <FixedAssetsPage />;
  else if (path === '/budgets') page = <BudgetsPage />;
  else if (path.match(/^\/budgets\/[^/]+\/variance$/)) page = <BudgetVariancePage />;
  else if (path.startsWith('/budgets/')) page = <BudgetDetailPage />;
  else if (path === '/accounting/sasra-return') page = <SASRAReturnPage />;
  else if (path === '/accounting/dashboard') page = <FinanceDashboardPage />;
  else if (path === '/provisioning' || path.startsWith('/provisioning/')) page = <ProvisioningPage />;
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
