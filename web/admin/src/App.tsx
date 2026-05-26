import { AuthProvider, useAuth } from './auth/AuthContext';
import AppShell from './components/AppShell';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Tweaks } from './components/Tweaks';
import { PageCrumbProvider } from './lib/pageCrumb';
import Login from './pages/Login';
import Dashboard from './pages/Dashboard';
import ResetPassword from './pages/ResetPassword';
import AcceptInvite from './pages/AcceptInvite';
import Roles from './pages/Roles';
import Users from './pages/Users';
import Members from './pages/Members';
// MemberOnboarding + OrganizationOnboarding pages were deleted in
// Phase C — /applications/new is the single onboarding entry point
// for both individual and institutional applicants. /members/new
// and /orgs/new still resolve as 301-style redirects so any
// in-the-wild bookmarks land in the right place.
//
// MemberProfile + OrganizationProfile + Organizations list pages
// were deleted in the Phase D destructive drop — CounterpartyProfile
// is the only detail page; Members.tsx is the only register.
import CounterpartyProfile from './pages/CounterpartyProfile';
import ApplicationsQueuePage from './pages/Applications/ApplicationsQueue';
import NewApplicationPage from './pages/Applications/NewApplication';
import ApplicationDetailPage from './pages/Applications/ApplicationDetail';
import TenantOnboarding from './pages/TenantOnboarding';
import TenantProfile from './pages/TenantProfile';
import TenantSettings from './pages/TenantSettings';
import MpesaPaybillsPage from './pages/Settings/MpesaPaybills';
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
import FeesSummaryPage from './pages/Accounting/FeesSummary';
import ReconciliationPage from './pages/Accounting/Reconciliation';
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
import MpesaReconciliationPage from './pages/Accounting/MpesaReconciliation';
import MemberStatementPage from './pages/Members/MemberStatement';
import ProvisioningPage from './pages/Loans/Provisioning';
import CollectionDesk from './pages/CollectionDesk';
import CollectionReceipts from './pages/CollectionReceipts';

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
  else if (path === '/members/new') {
    // Deleted in Phase C. Redirect via window.location.replace so
    // back-button history doesn't trap the user on a dead URL.
    window.location.replace('/applications/new');
    page = null;
  }
  else if (path === '/members') page = <Members />;
  else if (path.match(/^\/members\/[^/]+\/statement$/)) page = <MemberStatementPage />;
  else if (path.startsWith('/members/')) page = <CounterpartyProfile />;
  else if (path === '/tenants/new') page = <TenantOnboarding />;
  else if (path.startsWith('/tenants/')) page = <TenantProfile />;
  else if (path === '/settings/mpesa') page = <MpesaPaybillsPage />;
  else if (path === '/settings') page = <TenantSettings />;
  else if (path === '/orgs/new') {
    window.location.replace('/applications/new?kind=institutional');
    page = null;
  }
  // /orgs (list) is a saved filter over /members; redirect rather
  // than render a separate page. /orgs/:id (detail) routes to the
  // unified CounterpartyProfile.
  else if (path === '/orgs') {
    window.location.replace('/members?kind=institutional');
    page = null;
  }
  else if (path.startsWith('/orgs/')) page = <CounterpartyProfile />;
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
  else if (path === '/accounting/fees-summary') page = <FeesSummaryPage />;
  else if (path === '/accounting/reconciliation') page = <ReconciliationPage />;
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
  else if (path === '/accounting/mpesa-reconciliation') page = <MpesaReconciliationPage />;
  else if (path === '/provisioning' || path.startsWith('/provisioning/')) page = <ProvisioningPage />;
  else if (path === '/collect') page = <CollectionDesk />;
  else if (path === '/collect/receipts' || path.startsWith('/collect/receipts/')) page = <CollectionReceipts />;
  else page = <Dashboard />;

  return <AppShell>{page}</AppShell>;
}

export default function App() {
  return (
    <AuthProvider>
      <PageCrumbProvider>
        <Gate />
        <Tweaks />
      </PageCrumbProvider>
    </AuthProvider>
  );
}
