// API client for the admin WebGUI and operator UI: JSON, CSRF header on writes, uniform
// error objects whose code is translated via `errors.<code>`.
import { has, t } from '@opensignage/i18n';

let csrfToken = '';
let onUnauthorized: (() => void) | null = null;

export function setCsrf(token: string): void {
  csrfToken = token;
}

export function setUnauthorizedHandler(fn: () => void): void {
  onUnauthorized = fn;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public params: Record<string, unknown> = {},
  ) {
    super(message);
  }

  /** Human readable, translated message. */
  get text(): string {
    return translateError(this.code, this.params, this.message);
  }
}

export function translateError(code: string, params: Record<string, unknown>, fallback: string): string {
  const p: Record<string, string | number> = {};
  for (const [k, v] of Object.entries(params)) p[k] = typeof v === 'number' ? v : Array.isArray(v) ? v.join(', ') : String(v);
  if (code === 'invalid_field' && typeof params.field === 'string') {
    const reasonKey = `errors.field.${String(params.reason)}`;
    const field = has(`fields.${params.field}`) ? t(`fields.${params.field}`) : params.field;
    return has(reasonKey) ? t(reasonKey, { ...p, field }) : t('errors.invalid_field', { ...p, field });
  }
  const key = `errors.${code}`;
  if (has(key)) return t(key, p);
  return fallback || t('errors.unknown');
}

export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  raw?: boolean;
  headers?: Record<string, string>;
  /**
   * Lets the request outlive the page (tab closed or reloaded). Browsers cap keepalive
   * bodies at 64 KiB; larger bodies are sent as a normal request (best effort).
   */
  keepalive?: boolean;
}

/** Keepalive body limit of the browsers (64 KiB for all pending keepalive requests). */
export const KEEPALIVE_MAX_BYTES = 60 * 1024;

export async function api<T = unknown>(path: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? (opts.body !== undefined ? 'POST' : 'GET');
  const headers: Record<string, string> = { Accept: 'application/json', ...opts.headers };
  let body: BodyInit | undefined;
  if (opts.body instanceof FormData || opts.body instanceof Blob) {
    body = opts.body;
  } else if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  if (method !== 'GET' && method !== 'HEAD' && csrfToken) headers['X-CSRF-Token'] = csrfToken;
  const keepalive = !!opts.keepalive && (body === undefined || (typeof body === 'string' && new TextEncoder().encode(body).length <= KEEPALIVE_MAX_BYTES));
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body, signal: opts.signal, credentials: 'same-origin', keepalive });
  } catch (e) {
    if ((e as Error).name === 'AbortError') throw e;
    throw new ApiError(0, 'network', t('errors.network'));
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; params?: Record<string, unknown> } })?.error;
    const apiErr = new ApiError(res.status, err?.code ?? `http_${res.status}`, err?.message ?? res.statusText, err?.params ?? {});
    if (res.status === 401 && onUnauthorized) onUnauthorized();
    throw apiErr;
  }
  return data as T;
}

export const get = <T>(path: string, signal?: AbortSignal): Promise<T> => api<T>(path, { signal });
export const post = <T>(path: string, body?: unknown): Promise<T> => api<T>(path, { method: 'POST', body: body ?? {} });
export const put = <T>(path: string, body: unknown): Promise<T> => api<T>(path, { method: 'PUT', body });
export const patch = <T>(path: string, body: unknown): Promise<T> => api<T>(path, { method: 'PATCH', body });
export const del = <T>(path: string): Promise<T> => api<T>(path, { method: 'DELETE' });

export function errorText(e: unknown): string {
  if (e instanceof ApiError) return e.text;
  if (e instanceof Error) return e.message;
  return String(e);
}

/**
 * Uploads a large raw body with progress (fetch has no upload progress). Same CSRF and
 * error handling as api().
 */
export function uploadWithProgress<T = unknown>(path: string, body: Blob, onProgress: (loaded: number, total: number) => void, contentType = 'application/octet-stream'): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', path);
    xhr.setRequestHeader('Accept', 'application/json');
    xhr.setRequestHeader('Content-Type', contentType);
    if (csrfToken) xhr.setRequestHeader('X-CSRF-Token', csrfToken);
    xhr.upload.onprogress = (e) => onProgress(e.loaded, e.lengthComputable ? e.total : body.size);
    xhr.onerror = () => reject(new ApiError(0, 'network', t('errors.network')));
    xhr.onabort = () => reject(new DOMException('aborted', 'AbortError'));
    xhr.onload = () => {
      let data: unknown = undefined;
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : undefined;
      } catch {
        data = undefined;
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data as T);
        return;
      }
      const err = (data as { error?: { code?: string; message?: string; params?: Record<string, unknown> } })?.error;
      if (xhr.status === 401 && onUnauthorized) onUnauthorized();
      reject(new ApiError(xhr.status, err?.code ?? `http_${xhr.status}`, err?.message ?? xhr.statusText, err?.params ?? {}));
    };
    xhr.send(body);
  });
}
