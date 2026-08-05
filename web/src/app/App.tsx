import { useEffect, useState } from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { ToastProvider } from '../components/Toast';
import { LoadingState } from '../components/ui';
import { AppShell } from '../layout/AppShell';
import { api, AUTH_EXPIRED_EVENT } from '../lib/api';
import type { User } from '../lib/types';
import { KeysPage } from '../pages/KeysPage';
import { LoginPage } from '../pages/LoginPage';
import { LogsPage } from '../pages/LogsPage';
import { MonitorPage } from '../pages/MonitorPage';
import { RoutesPage } from '../pages/RoutesPage';
import { SettingsPage } from '../pages/SettingsPage';
import { UpstreamsPage } from '../pages/UpstreamsPage';

export function App() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    api.me().then(setUser).catch(() => setUser(null)).finally(() => setChecking(false));
  }, []);

  useEffect(() => {
    const handleExpiredSession = () => setUser(null);
    window.addEventListener(AUTH_EXPIRED_EVENT, handleExpiredSession);
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, handleExpiredSession);
  }, []);

  const login = async (password: string) => {
    const nextUser = await api.login(password);
    setUser(nextUser);
    return nextUser;
  };

  const logout = async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
    }
  };

  if (checking) return <div className="boot-screen"><div className="brand-mark">J</div><LoadingState label="正在连接 JieShan" /></div>;

  if (!user) return <LoginPage onLogin={login} onDemo={async () => setUser(await api.me())} />;

  return (
    <ToastProvider>
      <BrowserRouter>
        <Routes>
          <Route element={<AppShell user={user} onLogout={logout} />}>
            <Route index element={<MonitorPage />} />
            <Route path="upstreams" element={<UpstreamsPage />} />
            <Route path="routes" element={<RoutesPage />} />
            <Route path="keys" element={<KeysPage />} />
            <Route path="logs" element={<LogsPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </ToastProvider>
  );
}
