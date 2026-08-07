import { useEffect, useState } from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { ToastProvider } from '../components/Toast';
import { LoadingState } from '../components/ui';
import { AppShell } from '../layout/AppShell';
import { api, AUTH_EXPIRED_EVENT } from '../lib/api';
import type { User } from '../lib/types';
import { DashboardPage } from '../pages/DashboardPage';
import { KeysPage } from '../pages/KeysPage';
import { LoginPage } from '../pages/LoginPage';
import { LogsPage } from '../pages/LogsPage';
import { ModelsPage } from '../pages/ModelsPage';
import { MonitorPage } from '../pages/MonitorPage';
import { SettingsPage } from '../pages/SettingsPage';
import { SiteDetailPage } from '../pages/SiteDetailPage';
import { SitesPage } from '../pages/SitesPage';

export function App() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    api.me().then(setUser).catch(() => setUser(null)).finally(() => setChecking(false));
  }, []);

  useEffect(() => {
    const expired = () => setUser(null);
    window.addEventListener(AUTH_EXPIRED_EVENT, expired);
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, expired);
  }, []);

  const login = async (password: string) => {
    const next = await api.login(password);
    setUser(next);
  };

  const logout = async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
    }
  };

  if (checking) {
    return (
      <div className="boot-screen">
        <img src="/jieshan-brand.jpg" alt="" />
        <LoadingState label="正在连接管理端" />
      </div>
    );
  }

  if (!user) return <LoginPage onLogin={login} />;

  return (
    <ToastProvider>
      <BrowserRouter>
        <Routes>
          <Route element={<AppShell user={user} onLogout={logout} />}>
            <Route index element={<Navigate to="/overview" replace />} />
            <Route path="overview" element={<DashboardPage />} />
            <Route path="sites" element={<SitesPage />} />
            <Route path="sites/:siteId" element={<SiteDetailPage />} />
            <Route path="models" element={<ModelsPage />} />
            <Route path="monitor" element={<MonitorPage />} />
            <Route path="keys" element={<KeysPage />} />
            <Route path="logs" element={<LogsPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/overview" replace />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ToastProvider>
  );
}
