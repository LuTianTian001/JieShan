import {
  Activity,
  Cable,
  ChevronLeft,
  CircleGauge,
  FileClock,
  KeyRound,
  LogOut,
  Menu,
  Network,
  Settings,
  X,
} from 'lucide-react';
import { useMemo, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { isDemoMode } from '../lib/api';
import type { User } from '../lib/types';
import { Badge, IconButton } from '../components/ui';

const navigation = [
  { to: '/', label: '监控', icon: Activity },
  { to: '/upstreams', label: '上游', icon: Cable },
  { to: '/routes', label: '路由', icon: Network },
  { to: '/keys', label: '密钥', icon: KeyRound },
  { to: '/logs', label: '日志', icon: FileClock },
  { to: '/settings', label: '设置', icon: Settings },
];

const pageTitles: Record<string, string> = {
  '/': '运行监控',
  '/upstreams': '上游管理',
  '/routes': '模型路由',
  '/keys': '下游密钥',
  '/logs': '请求日志',
  '/settings': '系统设置',
};

export function AppShell({ user, onLogout }: { user: User; onLogout: () => Promise<void> }) {
  const [collapsed, setCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
  const title = useMemo(() => pageTitles[location.pathname] || 'JieShan', [location.pathname]);

  return (
    <div className={`app-shell ${collapsed ? 'sidebar-collapsed' : ''}`}>
      <aside className={`sidebar ${mobileOpen ? 'is-mobile-open' : ''}`}>
        <div className="sidebar-brand">
          <div className="brand-mark" aria-hidden="true"><img src="/jieshan-brand.jpg" alt="" /></div>
          {!collapsed && <div className="brand-copy"><strong>JieShan</strong><span>API Gateway</span></div>}
          <IconButton className="mobile-close" label="关闭导航" onClick={() => setMobileOpen(false)}><X size={18} /></IconButton>
        </div>
        <nav className="sidebar-nav" aria-label="主要导航">
          {navigation.map((item) => {
            const Icon = item.icon;
            return (
              <NavLink key={item.to} to={item.to} end={item.to === '/'} title={collapsed ? item.label : undefined} onClick={() => setMobileOpen(false)}>
                <Icon size={18} aria-hidden="true" />
                {!collapsed && <span>{item.label}</span>}
              </NavLink>
            );
          })}
        </nav>
        <button type="button" className="sidebar-collapse" onClick={() => setCollapsed((value) => !value)}>
          <ChevronLeft className={collapsed ? 'is-flipped' : ''} size={16} />
          {!collapsed && <span>收起侧栏</span>}
        </button>
      </aside>

      {mobileOpen && <button type="button" className="mobile-scrim" aria-label="关闭导航" onClick={() => setMobileOpen(false)} />}

      <header className="topbar">
        <div className="topbar-leading">
          <IconButton className="mobile-menu" label="打开导航" onClick={() => setMobileOpen(true)}><Menu size={19} /></IconButton>
          <div><span className="topbar-eyebrow">JieShan</span><strong>{title}</strong></div>
        </div>
        <div className="topbar-actions">
          {isDemoMode() && <Badge tone="warning">预览数据</Badge>}
          <span className="runtime-state"><span />网关在线</span>
          <div className="user-chip"><span>{user.username.slice(0, 1).toUpperCase()}</span><strong>{user.username}</strong></div>
          <IconButton label="退出登录" onClick={() => void onLogout()}><LogOut size={17} /></IconButton>
        </div>
      </header>

      <main className="main-content">
        <Outlet />
      </main>
      <div className="connection-beacon" title="管理端连接正常"><CircleGauge size={14} /><span>在线</span></div>
    </div>
  );
}
