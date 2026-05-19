import { useAuth } from '../auth/AuthContext';
import { isPlatformHost } from '../auth/tenant';
import PlatformDashboard from './PlatformDashboard';
import TenantDashboard from './TenantDashboard';

export default function Dashboard() {
  const { user } = useAuth();
  const platform = isPlatformHost();
  if (platform && user?.is_platform_admin) return <PlatformDashboard />;
  return <TenantDashboard />;
}
