import { Navigate } from 'react-router';
import { LayoutDashboard, Settings as SettingsIcon } from 'lucide-react';
import type { Feature } from '../types';
import { UsersPage } from './Users';
import { CertificatePage, DefaultsPage, GeneralSettingsPage } from './Settings';
import { AuditPage, DashboardPage, ProfilePage } from './Misc';
import { BackupPage } from './Backup';
import { UpdatesPage } from './Updates';
import { StoragePage } from './Storage';
import { FontsPage } from './Fonts';

export const systemFeature: Feature = {
  routes: [
    { path: '/', element: <DashboardPage />, minRole: 'operator' },
    { path: '/profile', element: <ProfilePage />, minRole: 'user' },
    // "Settings" in the menu opens its first page (the group has no page of its own).
    { path: '/settings', element: <Navigate to="/settings/general" replace />, minRole: 'admin' },
    { path: '/settings/general', element: <GeneralSettingsPage />, minRole: 'admin' },
    { path: '/settings/defaults', element: <DefaultsPage />, minRole: 'admin' },
    { path: '/settings/storage', element: <StoragePage />, minRole: 'admin' },
    { path: '/settings/fonts', element: <FontsPage />, minRole: 'admin' },
    { path: '/settings/users', element: <UsersPage />, minRole: 'admin' },
    { path: '/settings/certificate', element: <CertificatePage />, minRole: 'admin' },
    { path: '/settings/audit', element: <AuditPage />, minRole: 'admin' },
    { path: '/settings/backup', element: <BackupPage />, minRole: 'admin' },
    { path: '/settings/updates', element: <UpdatesPage />, minRole: 'admin' },
  ],
  nav: [
    { key: 'dashboard', label: 'admin.nav.dashboard', icon: LayoutDashboard, path: '/', minRole: 'operator', order: 0 },
    {
      key: 'settings',
      label: 'admin.nav.settings',
      icon: SettingsIcon,
      path: '/settings',
      minRole: 'admin',
      order: 90,
      pinBottom: true,
      sub: [
        { key: 'general', label: 'admin.settings.general', path: '/settings/general', minRole: 'admin' },
        { key: 'defaults', label: 'admin.settings.defaults', path: '/settings/defaults', minRole: 'admin' },
        { key: 'storage', label: 'admin.settings.storage', path: '/settings/storage', minRole: 'admin' },
        { key: 'fonts', label: 'admin.settings.fonts', path: '/settings/fonts', minRole: 'admin' },
        { key: 'users', label: 'admin.users.title', path: '/settings/users', minRole: 'admin' },
        { key: 'certificate', label: 'admin.cert.title', path: '/settings/certificate', minRole: 'admin' },
        { key: 'backup', label: 'backup.title', path: '/settings/backup', minRole: 'admin' },
        { key: 'updates', label: 'updates.title', path: '/settings/updates', minRole: 'admin' },
        { key: 'audit', label: 'admin.audit.title', path: '/settings/audit', minRole: 'admin' },
      ],
    },
  ],
};
