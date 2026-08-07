import {
  HeartPulse,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Menu,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Server,
  ServerCog,
  Settings2,
  Waypoints,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { IconButton, cn } from '../components/ui';
import type { User } from '../lib/types';

const primaryNavigation = [
  { to: '/overview', label: '总览', icon: LayoutDashboard },
  { to: '/sites', label: '上游站点', icon: Server },
  { to: '/models', label: '模型与路由', icon: Waypoints },
  { to: '/monitor', label: '模型监控', icon: HeartPulse },
  { to: '/keys', label: '下游密钥', icon: KeyRound },
  { to: '/logs', label: '调用日志', icon: ScrollText },
  { to: '/system-logs', label: '系统日志', icon: ServerCog },
];

const pageTitles: Array<[RegExp, string]> = [
  [/^\/overview$/, '总览'],
  [/^\/sites\/\d+$/, '上游站点详情'],
  [/^\/sites$/, '上游站点'],
  [/^\/models$/, '模型与路由'],
  [/^\/monitor$/, '模型监控'],
  [/^\/keys$/, '下游密钥'],
  [/^\/logs$/, '调用日志'],
  [/^\/system-logs$/, '系统日志'],
  [/^\/settings$/, '设置'],
];

export function AppShell({ user, onLogout }: { user: User; onLogout: () => Promise<void> }) {
  const [compact, setCompact] = useState(() => localStorage.getItem('jieshan.nav.compact') === '1');
  const [mobileOpen, setMobileOpen] = useState(false);
  const prototypeMode = document.documentElement.dataset.prototype === 'true';
  const location = useLocation();
  const title = useMemo(
    () => pageTitles.find(([pattern]) => pattern.test(location.pathname))?.[1] || 'JieShan',
    [location.pathname],
  );

  useEffect(() => setMobileOpen(false), [location.pathname]);
  useEffect(() => localStorage.setItem('jieshan.nav.compact', compact ? '1' : '0'), [compact]);
  useEffect(() => {
    if (!mobileOpen) return undefined;
    const previousOverflow = document.body.style.overflow;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMobileOpen(false);
    };
    document.body.style.overflow = 'hidden';
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.body.style.overflow = previousOverflow;
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [mobileOpen]);

  return (
    <div className={cn('app-shell', compact && 'nav-compact')}>
      <a className="skip-link" href="#main-content">跳到主要内容</a>

      <aside className={cn('sidebar', mobileOpen && 'is-open')} aria-label="JieShan 导航">
        <div className="brand-row">
          <img className="brand-avatar" src="/jieshan-brand.jpg" alt="" />
          <div className="brand-name"><strong>JieShan</strong><span>AI Gateway</span></div>
          <IconButton className="mobile-nav-close" label="关闭导航" onClick={() => setMobileOpen(false)}><X size={18} /></IconButton>
        </div>

        <nav className="main-navigation" aria-label="主要导航">
          {primaryNavigation.map(({ to, label, icon: Icon }) => (
            <NavLink key={to} to={to} title={compact ? label : undefined}>
              <Icon size={18} aria-hidden="true" />
              <span>{label}</span>
            </NavLink>
          ))}
        </nav>

        <div className="sidebar-footer">
          <NavLink to="/settings" title={compact ? '设置' : undefined}>
            <Settings2 size={18} aria-hidden="true" />
            <span>设置</span>
          </NavLink>
          <button
            className="nav-collapse"
            type="button"
            aria-label={compact ? '展开侧栏' : '收起侧栏'}
            title={compact ? '展开侧栏' : undefined}
            onClick={() => setCompact((value) => !value)}
          >
            {compact ? <PanelLeftOpen size={18} aria-hidden="true" /> : <PanelLeftClose size={18} aria-hidden="true" />}
            <span>{compact ? '展开侧栏' : '收起侧栏'}</span>
          </button>
        </div>
      </aside>

      {mobileOpen && <button type="button" className="sidebar-scrim" aria-label="关闭导航" onClick={() => setMobileOpen(false)} />}

      <header className="topbar">
        <div className="topbar-leading">
          <IconButton className="mobile-nav-trigger" label="打开导航" onClick={() => setMobileOpen(true)}><Menu size={19} /></IconButton>
          <strong className="mobile-page-title">{title}</strong>
        </div>
        <div className="topbar-account">
          {prototypeMode && <span className="prototype-pill">前端原型</span>}
          <span className="user-identity" title={user.username}>
            <span className="user-badge" aria-hidden="true">{user.username.slice(0, 1).toUpperCase()}</span>
            <span className="user-name">{user.username}</span>
          </span>
          <IconButton label="退出登录" onClick={() => void onLogout()}><LogOut size={17} /></IconButton>
        </div>
      </header>

      <main className="main-content" id="main-content" tabIndex={-1}><Outlet /></main>
    </div>
  );
}
