// The admin WebGUI and the operator UI are built into the server. After a server update an
// open page would keep running its old code against the new server (whose login sessions
// have ended), so it reloads itself as soon as the server reports another version. The login
// page then says why (updatedTo).
import { isConnected, isStarted, onConnectionChange } from './events';

const UPDATED_KEY = 'os.updatedTo';
const RELOADED_KEY = 'os.reloadedFor';
const FAST = 3000; // connection lost: the server is probably restarting
const SLOW = 60000;

/** Version of the running server (public endpoint), null when unreachable. */
export async function fetchServerVersion(): Promise<string | null> {
  try {
    const res = await fetch('/api/v1/system/info', { cache: 'no-store', credentials: 'same-origin' });
    if (!res.ok) return null;
    const info = (await res.json()) as { version?: unknown };
    return typeof info.version === 'string' && info.version ? info.version : null;
  } catch {
    return null;
  }
}

/** Reloads the page for a new server version (at most once per change within 30 s, so a
 *  stale page cache can never cause a reload loop). */
export function reloadForUpdate(from: string, to: string, reload: () => void = () => location.reload()): void {
  const mark = `${from}>${to}`;
  try {
    const last = JSON.parse(sessionStorage.getItem(RELOADED_KEY) ?? 'null') as { mark: string; at: number } | null;
    if (last && last.mark === mark && Date.now() - last.at < 30000) return;
    sessionStorage.setItem(RELOADED_KEY, JSON.stringify({ mark, at: Date.now() }));
    sessionStorage.setItem(UPDATED_KEY, to);
  } catch {
    // storage disabled: reload anyway, the login page just cannot say why
  }
  reload();
}

/** Version the server was updated to before this page was reloaded (for the login page). */
export function updatedTo(): string | null {
  try {
    return sessionStorage.getItem(UPDATED_KEY);
  } catch {
    return null;
  }
}

export function clearUpdated(): void {
  try {
    sessionStorage.removeItem(UPDATED_KEY);
  } catch {
    // nothing stored
  }
}

/** Watches the server version: often while the live connection is lost, once a minute
 *  otherwise, and whenever the page becomes visible again. Returns a stop function. */
export function watchServerVersion(
  loaded: string,
  onChange: (version: string) => void = (v) => reloadForUpdate(loaded, v),
  fetchVersion: () => Promise<string | null> = fetchServerVersion,
): () => void {
  let stopped = false;
  let timer: number | undefined;
  let checking = false;
  const check = async (): Promise<void> => {
    if (stopped || checking) return;
    checking = true;
    try {
      const v = await fetchVersion();
      if (!stopped && v && v !== loaded) {
        stopped = true;
        onChange(v);
      }
    } finally {
      checking = false;
    }
  };
  const schedule = (): void => {
    window.clearTimeout(timer);
    if (stopped) return;
    timer = window.setTimeout(() => void check().then(schedule), isStarted() && !isConnected() ? FAST : SLOW);
  };
  const onVisible = (): void => {
    if (document.visibilityState === 'visible') void check();
  };
  const offConn = onConnectionChange(schedule);
  document.addEventListener('visibilitychange', onVisible);
  window.addEventListener('pageshow', onVisible);
  schedule();
  return () => {
    stopped = true;
    window.clearTimeout(timer);
    offConn();
    document.removeEventListener('visibilitychange', onVisible);
    window.removeEventListener('pageshow', onVisible);
  };
}
