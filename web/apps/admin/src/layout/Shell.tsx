import { useEffect, useState } from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router';
import { AlertTriangle, ChevronDown, ChevronRight, LogOut, MonitorSmartphone, UserCircle2 } from 'lucide-react';
import type { NavDef } from '../features/types';
import { useStoredState, useT, useQuery } from '../lib/hooks';
import { roleAtLeast, useSession } from '../lib/session';
import { useRights } from '../lib/rights';
import { useConnected, useEvents } from '../lib/events';
import { countBySeverity, openAlerts, type Alert } from '../features/devices/model';
import iconUrl from '../../../../../branding/Icon.png';
import logoUrl from '../../../../../branding/logo_horizontal.svg';

interface AlertsResponse {
  count: number;
  alerts: Alert[];
}

function useAlerts(): number {
  const { user } = useSession();
  const q = useQuery<AlertsResponse>(user ? '/api/v1/alerts' : null);
  useEvents(['devices', 'updates'], () => q.reload());
  useEffect(() => {
    const id = window.setInterval(q.reload, 30000);
    return () => clearInterval(id);
  }, [q.reload]);
  // Only errors show in the sidebar; warnings (e.g. a screen switched off at night) are
  // listed on the dashboard and the problems page. 404 = devices module not available yet.
  return countBySeverity(openAlerts(q.data?.alerts)).critical;
}

function isActive(item: NavDef, pathname: string): boolean {
  return pathname === item.path || pathname.startsWith(item.path + '/');
}

function NavGroup({ item, collapsed, open, onToggle }: { item: NavDef; collapsed: boolean; open: boolean; onToggle: () => void }) {
  const t = useT();
  const { user } = useSession();
  const rights = useRights();
  const location = useLocation();
  const children = item.useChildren ? item.useChildren() : [];
  const subs = (item.sub ?? []).filter((s) => roleAtLeast(user?.role, s.minRole) && (!s.visible || s.visible(rights)));
  const active = isActive(item, location.pathname);
  const hasKids = children.length > 0 || subs.length > 0;
  const Icon = item.icon;
  return (
    <div className="nav-group">
      <div className={`nav-item ${active ? 'active' : ''}`}>
        <NavLink to={item.path} end={false} className="nav-link" title={t(item.label)}>
          <Icon size={18} />
          {!collapsed && <span>{t(item.label)}</span>}
        </NavLink>
        {!collapsed && hasKids && (
          <button className="nav-toggle" aria-label={t(item.label)} aria-expanded={open} onClick={onToggle}>
            {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          </button>
        )}
      </div>
      {!collapsed && open && hasKids && (
        <div className="nav-children">
          {subs.map((s) => (
            <NavLink key={s.key} to={s.path} end className="nav-child">
              {t(s.label)}
            </NavLink>
          ))}
          {children.map((c) => (
            <NavLink key={c.key} to={c.path} end className="nav-child" title={c.label}>
              {c.label}
            </NavLink>
          ))}
        </div>
      )}
    </div>
  );
}

export function Shell({ nav, fullscreen, children }: { nav: NavDef[]; fullscreen: boolean; children: React.ReactNode }) {
  const t = useT();
  const navigate = useNavigate();
  const { user, server, logout } = useSession();
  const [collapsedPref, setCollapsed] = useStoredState('os.sidebar.collapsed', false);
  const collapsed = fullscreen || collapsedPref;
  const alerts = useAlerts();
  const connected = useConnected();
  const [online, setOnline] = useState(navigator.onLine);
  useEffect(() => {
    const on = (): void => setOnline(navigator.onLine);
    window.addEventListener('online', on);
    window.addEventListener('offline', on);
    return () => {
      window.removeEventListener('online', on);
      window.removeEventListener('offline', on);
    };
  }, []);
  const rights = useRights();
  const allowed = nav.filter((n) => roleAtLeast(user?.role, n.minRole) && (!n.visible || n.visible(rights))).sort((a, b) => a.order - b.order);
  const items = allowed.filter((n) => !n.pinBottom);
  const pinned = allowed.filter((n) => n.pinBottom);
  // At most one group is open (settings at the bottom included): opening one closes the
  // others, and every navigation opens the group of the current page again.
  const location = useLocation();
  const activeKey = allowed.find((n) => isActive(n, location.pathname))?.key ?? null;
  const [openKey, setOpenKey] = useState<string | null>(activeKey);
  useEffect(() => setOpenKey(activeKey), [activeKey, location.key]);
  const group = (n: NavDef): React.ReactNode => (
    <NavGroup key={n.key} item={n} collapsed={collapsed} open={openKey === n.key} onToggle={() => setOpenKey(openKey === n.key ? null : n.key)} />
  );
  return (
    <div className={`shell ${collapsed ? 'collapsed' : ''}`}>
      <aside className="sidebar">
        <button className="sidebar-logo" onClick={() => !fullscreen && setCollapsed(!collapsedPref)} title={t('admin.nav.toggle')}>
          <img src={collapsed ? iconUrl : logoUrl} alt="OpenSignage" />
        </button>
        {!collapsed && <div className="sidebar-version">server v{server?.version}</div>}
        <nav className="nav">
          {items.map(group)}
          <a className="nav-item nav-link" href="/control" title={t('admin.nav.control')}>
            <MonitorSmartphone size={18} />
            {!collapsed && <span>{t('admin.nav.control')}</span>}
          </a>
        </nav>
        <div className="sidebar-bottom">
          {pinned.map(group)}
          {alerts > 0 && (
            <button className="alert-marker" onClick={() => navigate('/alerts')} title={t('admin.alerts.title', { count: alerts })}>
              <AlertTriangle size={18} />
              {!collapsed && <span>{t('admin.alerts.title', { count: alerts })}</span>}
            </button>
          )}
          <NavLink to="/profile" className="nav-item nav-link" title={user?.displayName}>
            <UserCircle2 size={18} />
            {!collapsed && <span className="ellipsis">{user?.displayName}</span>}
          </NavLink>
          <button className="nav-item nav-link" onClick={() => void logout()} title={t('admin.nav.logout')}>
            <LogOut size={18} />
            {!collapsed && <span>{t('admin.nav.logout')}</span>}
          </button>
        </div>
      </aside>
      <main className={`main ${fullscreen ? 'main-full' : ''}`}>
        {(!connected || !online) && <div className="banner banner-warning">{t('common.state.offline')}</div>}
        {children}
      </main>
    </div>
  );
}
