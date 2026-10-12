import { createContext, type ReactNode, useCallback, useContext, useEffect, useRef, useState } from 'react';
import { isLanguage, setLanguage } from '@opensignage/i18n';
import type { Role } from '@opensignage/contracts';
import { get, post, setCsrf, setUnauthorizedHandler } from './api';
import { clearUpdated, reloadForUpdate, watchServerVersion } from './serverWatch';

export interface SessionUser {
  id: string;
  username: string;
  displayName: string;
  role: Role;
  language: string;
  zoneIds: string[];
}

export interface ServerInfo {
  version: string;
  name: string;
  language: string;
  timezone: string;
  currency: string;
}

interface SessionResponse {
  needsSetup: boolean;
  server: ServerInfo;
  user?: SessionUser;
  csrf?: string;
}

interface SessionCtx {
  ready: boolean;
  needsSetup: boolean;
  user: SessionUser | null;
  server: ServerInfo | null;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = createContext<SessionCtx | null>(null);

const LANG_KEY = 'os.language';

/** Browser override of the UI language (per browser, applied on save only). */
export function storedLanguage(): string | null {
  try {
    return localStorage.getItem(LANG_KEY);
  } catch {
    return null;
  }
}

export function storeLanguage(code: string | null): void {
  try {
    if (code) localStorage.setItem(LANG_KEY, code);
    else localStorage.removeItem(LANG_KEY);
  } catch {
    // storage disabled: language falls back to the account/server default
  }
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<Omit<SessionCtx, 'refresh' | 'logout'>>({ ready: false, needsSetup: false, user: null, server: null });
  // Server version this page was loaded with: another version means the server was updated
  // and the page must reload (serverWatch.ts).
  const loaded = useRef<string | null>(null);
  const [watchFrom, setWatchFrom] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    const s = await get<SessionResponse>('/api/v1/auth/session');
    if (loaded.current === null) {
      loaded.current = s.server.version;
      setWatchFrom(s.server.version);
    } else if (s.server.version !== loaded.current) {
      reloadForUpdate(loaded.current, s.server.version);
      return;
    }
    if (s.user) clearUpdated();
    setCsrf(s.csrf ?? '');
    // An account without an own language has language '' and follows the server default.
    const lang = storedLanguage() || s.user?.language || s.server.language;
    await setLanguage(isLanguage(lang) ? lang : 'en');
    setState({ ready: true, needsSetup: s.needsSetup, user: s.user ?? null, server: s.server });
  }, []);

  const logout = useCallback(async () => {
    try {
      await post('/api/v1/auth/logout');
    } finally {
      setCsrf('');
      setState((s) => ({ ...s, user: null }));
    }
  }, []);

  useEffect(() => (watchFrom ? watchServerVersion(watchFrom) : undefined), [watchFrom]);

  useEffect(() => {
    setUnauthorizedHandler(() => setState((s) => (s.user ? { ...s, user: null } : s)));
    refresh().catch(() => {
      // Server unreachable at start: show the login page with an error; retry on submit.
      setState((s) => ({ ...s, ready: true }));
    });
  }, [refresh]);

  return <Ctx.Provider value={{ ...state, refresh, logout }}>{children}</Ctx.Provider>;
}

export function useSession(): SessionCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error('SessionProvider missing');
  return c;
}

export function roleAtLeast(role: Role | undefined, min: Role): boolean {
  const rank: Record<Role, number> = { user: 1, operator: 2, admin: 3 };
  return !!role && rank[role] >= rank[min];
}
