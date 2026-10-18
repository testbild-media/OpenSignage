import { useEffect } from 'react';
import { createBrowserRouter, Navigate, Route, RouterProvider, Routes, useLocation } from 'react-router';
import { features } from './features';
import { Shell } from './layout/Shell';
import { LoginPage, SetupPage } from './pages/Auth';
import { roleAtLeast, SessionProvider, useSession } from './lib/session';
import { ErrorBoundary, FeedbackProvider } from './components/feedback';
import { Spinner } from './components/ui';
import { useT } from './lib/hooks';
import { startEvents, stopEvents, useEvents } from './lib/events';
import { useRights } from './lib/rights';

const routes = features.flatMap((f) => f.routes);
const nav = features.flatMap((f) => f.nav);

function Routed() {
  const t = useT();
  const { ready, needsSetup, user, refresh } = useSession();
  const rights = useRights();
  const location = useLocation();
  useEffect(() => {
    if (user) startEvents();
    else stopEvents();
  }, [user]);
  // Changed own role or rights: load them again (pages and navigation follow).
  useEvents(['users'], (e) => {
    if (user && e.id === user.id) void refresh().catch(() => undefined);
  });
  if (!ready) return <Spinner label={t('common.state.loading')} />;
  if (needsSetup) return <SetupPage />;
  if (!user) return <LoginPage />;
  if (user.role === 'user') {
    // Role "user" works only in the operator UI.
    window.location.replace('/control');
    return <Spinner />;
  }
  const allowed = routes.filter((r) => roleAtLeast(user.role, r.minRole) && (!r.visible || r.visible(rights)));
  const current = allowed.find((r) => r.fullscreen && matchPath(r.path, location.pathname));
  return (
    <Shell nav={nav} fullscreen={!!current}>
      <ErrorBoundary key={location.pathname} label={t('common.state.error')} retryLabel={t('common.action.retry')}>
        <Routes>
          {allowed.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </ErrorBoundary>
    </Shell>
  );
}

function matchPath(pattern: string, path: string): boolean {
  const re = new RegExp('^' + pattern.replace(/:[^/]+/g, '[^/]+') + '/?$');
  return re.test(path);
}

// A data router so pages can hold a navigation until they are done (useBlocker: the editor
// saves before leaving). The feature routes stay declarative below the catch-all route.
const router = createBrowserRouter([{ path: '*', element: <Routed /> }]);

export function App() {
  const t = useT();
  return (
    <ErrorBoundary label={t('common.state.error')} retryLabel={t('common.action.retry')}>
      <SessionProvider>
        <FeedbackProvider>
          <RouterProvider router={router} />
        </FeedbackProvider>
      </SessionProvider>
    </ErrorBoundary>
  );
}
