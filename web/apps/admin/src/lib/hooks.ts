import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { getLanguage, onLanguageChange, t } from '@opensignage/i18n';
import { ApiError, get } from './api';

/** Re-renders on language change and returns the translate function. */
export function useT(): typeof t {
  useSyncExternalStore(onLanguageChange, getLanguage);
  return t;
}

export interface Query<T> {
  data: T | undefined;
  error: ApiError | Error | null;
  loading: boolean;
  reload: () => void;
  setData: (d: T) => void;
}

/**
 * Loads data with abort on unmount/path change. A slow response for an older request
 * never overwrites a newer one.
 */
export function useQuery<T>(path: string | null, deps: unknown[] = []): Query<T> {
  const [state, setState] = useState<{ data: T | undefined; error: ApiError | Error | null; loading: boolean }>({
    data: undefined,
    error: null,
    loading: path !== null,
  });
  const [tick, setTick] = useState(0);
  const gen = useRef(0);
  useEffect(() => {
    if (path === null) return;
    const my = ++gen.current;
    const ctrl = new AbortController();
    setState((s) => ({ ...s, loading: true, error: null }));
    get<T>(path, ctrl.signal).then(
      (data) => {
        if (my === gen.current) setState({ data, error: null, loading: false });
      },
      (error: Error) => {
        if (error.name === 'AbortError' || my !== gen.current) return;
        setState((s) => ({ ...s, error, loading: false }));
      },
    );
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, tick, ...deps]);
  const reload = useCallback(() => setTick((x) => x + 1), []);
  const setData = useCallback((data: T) => setState({ data, error: null, loading: false }), []);
  return { ...state, reload, setData };
}

/** Runs an async action once at a time (blocks double submit / double tap). */
export function useAction<A extends unknown[], R>(fn: (...args: A) => Promise<R>): {
  run: (...args: A) => Promise<R | undefined>;
  busy: boolean;
  error: unknown;
  clearError: () => void;
} {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const lock = useRef(false);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true; // StrictMode mounts twice in development
    return () => {
      mounted.current = false;
    };
  }, []);
  const run = useCallback(async (...args: A): Promise<R | undefined> => {
    if (lock.current) return undefined;
    lock.current = true;
    setBusy(true);
    setError(null);
    try {
      return await fnRef.current(...args);
    } catch (e) {
      if (mounted.current) setError(e);
      return undefined;
    } finally {
      lock.current = false;
      if (mounted.current) setBusy(false);
    }
  }, []);
  return { run, busy, error, clearError: () => setError(null) };
}

/** Persists a small UI preference in localStorage (sidebar state etc.). */
export function useStoredState<T>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      const s = localStorage.getItem(key);
      return s ? (JSON.parse(s) as T) : initial;
    } catch {
      return initial;
    }
  });
  const set = useCallback(
    (next: T) => {
      setV(next);
      try {
        localStorage.setItem(key, JSON.stringify(next));
      } catch {
        // storage full or disabled: the preference simply is not remembered
      }
    },
    [key],
  );
  return [v, set];
}
