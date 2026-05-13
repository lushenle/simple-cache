import { Routes, Route, Navigate } from 'react-router-dom';
import { AuthProvider, useAuth } from './hooks/useAuth';
import { useI18n } from './i18n/I18nProvider';
import Layout from './components/layout/Layout';
import Dashboard from './pages/Dashboard';
import Cluster from './pages/Cluster';
import CacheBrowser from './pages/CacheBrowser';
import KeyDetail from './pages/KeyDetail';
import Subscriptions from './pages/Subscriptions';
import Operations from './pages/Operations';
import Settings from './pages/Settings';
import Login from './pages/Login';

function ProtectedRoutes() {
  const { authenticated, checking } = useAuth();
  const { t } = useI18n();
  if (checking) {
    return (
      <div className="flex h-screen items-center justify-center">
        <div className="text-muted-foreground">{t('app.connecting')}</div>
      </div>
    );
  }
  if (!authenticated) return <Navigate to="/login" replace />;
  return (
    <Layout>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/cluster" element={<Cluster />} />
        <Route path="/cache" element={<CacheBrowser />} />
        <Route path="/cache/:key" element={<KeyDetail />} />
        <Route path="/subscriptions" element={<Subscriptions />} />
        <Route path="/operations" element={<Operations />} />
        <Route path="/settings" element={<Settings />} />
      </Routes>
    </Layout>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<ProtectedRoutes />} />
      </Routes>
    </AuthProvider>
  );
}
