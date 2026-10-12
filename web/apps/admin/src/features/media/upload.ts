// Resumable chunked upload (server: internal/server/media/uploads.go).
//
//   POST /api/v1/media/uploads {filename, size, folderId, replaceAssetId?} → {uploadId, received, chunkSize}
//   PUT  /api/v1/media/uploads/{id}  Content-Range: bytes start-end/total  (≤ 16 MB per request)
//        → {received, complete:false} | {complete:true, asset}
//   PUT  /api/v1/media/uploads/{id}  Content-Range: bytes */total  → current offset (finalizes a
//        fully received upload)
//   409 upload_offset {received} → the server tells where to continue.
//
// Network errors and 5xx answers are retried with backoff; before each retry the offset is
// asked from the server, so a chunk that arrived but whose answer was lost is never sent twice.
import { api, ApiError } from '../../lib/api';
import type { MediaAsset } from './model';

export const MAX_CHUNK = 16 * 1024 * 1024;
export const DEFAULT_CHUNK = 8 * 1024 * 1024;

interface StartResponse {
  uploadId: string;
  received: number;
  chunkSize?: number;
}

interface ChunkResponse {
  uploadId: string;
  received: number;
  complete: boolean;
  asset?: MediaAsset;
}

export interface UploadOptions {
  file: Blob;
  filename: string;
  folderId: string | null;
  /** Replace the file of this asset (the asset id stays the same). */
  replaceAssetId?: string;
  /** Continue an upload started earlier (after an error or a reload of the queue). */
  uploadId?: string;
  signal?: AbortSignal;
  /** Upper bound per request; the server limit (16 MB) always applies. */
  chunkSize?: number;
  /** Consecutive failed attempts before giving up (default 5). */
  maxRetries?: number;
  retryDelayMs?: (attempt: number) => number;
  onStart?: (uploadId: string) => void;
  onProgress?: (received: number, total: number) => void;
  onRetry?: (attempt: number, error: unknown) => void;
}

export interface UploadResult {
  uploadId: string;
  /** null when the upload finished but the answer got lost (the asset list shows it). */
  asset: MediaAsset | null;
}

export function isAbort(e: unknown): boolean {
  return typeof e === 'object' && e !== null && (e as { name?: string }).name === 'AbortError';
}

/** Temporary problems worth another attempt. Everything else is a definitive answer. */
export function isRetryable(e: unknown): boolean {
  if (!(e instanceof ApiError)) return false;
  if (e.status === 0) return true; // network
  if (e.status === 507) return false; // disk_full
  if (e.status >= 500 || e.status === 408) return true;
  return e.status === 400 && e.code === 'bad_range'; // chunk body cut off in transit
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason ?? new DOMException('aborted', 'AbortError'));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(timer);
      reject(signal?.reason ?? new DOMException('aborted', 'AbortError'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

const defaultDelay = (attempt: number): number => Math.min(30_000, 1000 * 2 ** (attempt - 1));

export async function uploadFile(o: UploadOptions): Promise<UploadResult> {
  const total = o.file.size;
  if (total <= 0) throw new ApiError(422, 'empty_file', 'the file is empty');
  const maxRetries = o.maxRetries ?? 5;
  const delay = o.retryDelayMs ?? defaultDelay;
  const signal = o.signal;
  let chunk = Math.max(1, Math.min(o.chunkSize ?? DEFAULT_CHUNK, MAX_CHUNK));
  let failures = 0;

  /** Runs fn, retrying temporary failures with backoff. */
  const retrying = async <T>(fn: () => Promise<T>): Promise<T> => {
    for (;;) {
      try {
        const r = await fn();
        failures = 0;
        return r;
      } catch (e) {
        if (isAbort(e) || !isRetryable(e) || ++failures > maxRetries) throw e;
        o.onRetry?.(failures, e);
        await sleep(delay(failures), signal);
      }
    }
  };

  let uploadId = o.uploadId;
  const url = (): string => `/api/v1/media/uploads/${encodeURIComponent(uploadId ?? '')}`;
  const query = (): Promise<ChunkResponse> =>
    api<ChunkResponse>(url(), { method: 'PUT', headers: { 'Content-Range': `bytes */${total}` }, signal });

  let offset = 0;
  if (uploadId) {
    const st = await retrying(query);
    if (st.complete) {
      o.onProgress?.(total, total);
      return { uploadId, asset: st.asset ?? null };
    }
    offset = st.received;
  } else {
    const st = await retrying(() =>
      api<StartResponse>('/api/v1/media/uploads', {
        method: 'POST',
        body: {
          filename: o.filename,
          size: total,
          folderId: o.folderId,
          ...(o.replaceAssetId ? { replaceAssetId: o.replaceAssetId } : {}),
        },
        signal,
      }),
    );
    uploadId = st.uploadId;
    offset = st.received || 0;
    if (st.chunkSize && st.chunkSize > 0) chunk = Math.min(chunk, st.chunkSize);
    o.onStart?.(uploadId);
  }
  o.onProgress?.(offset, total);

  for (;;) {
    signal?.throwIfAborted();
    // All bytes on the server but not finalized yet (e.g. the database was busy): ask again.
    const finalizing = offset >= total;
    const end = Math.min(offset + chunk, total) - 1;
    try {
      const r = finalizing
        ? await query()
        : await api<ChunkResponse>(url(), {
            method: 'PUT',
            body: o.file.slice(offset, end + 1),
            headers: { 'Content-Range': `bytes ${offset}-${end}/${total}` },
            signal,
          });
      failures = 0;
      offset = r.received;
      o.onProgress?.(offset, total);
      if (r.complete) return { uploadId, asset: r.asset ?? null };
    } catch (e) {
      if (isAbort(e)) throw e;
      if (e instanceof ApiError && e.code === 'upload_offset' && typeof e.params.received === 'number') {
        // The server has a different offset (lost answer, restored part file): continue there.
        if (++failures > maxRetries) throw e;
        offset = e.params.received;
        o.onProgress?.(offset, total);
        continue;
      }
      if (!isRetryable(e) || ++failures > maxRetries) throw e;
      o.onRetry?.(failures, e);
      await sleep(delay(failures), signal);
      try {
        const st = await query();
        if (st.complete) {
          o.onProgress?.(total, total);
          return { uploadId, asset: st.asset ?? null };
        }
        offset = st.received;
        o.onProgress?.(offset, total);
      } catch (qe) {
        if (isAbort(qe)) throw qe;
        // The last chunk arrived and the server finished the upload, only the answer was lost.
        if (qe instanceof ApiError && qe.status === 404 && (finalizing || end + 1 >= total))
          return { uploadId, asset: null };
        if (!isRetryable(qe)) throw qe;
        // still unreachable: try the same chunk again (counted on its next failure)
      }
    }
  }
}

/** Best-effort removal of an unfinished upload on the server. */
export async function abortUpload(uploadId: string): Promise<void> {
  try {
    await api(`/api/v1/media/uploads/${encodeURIComponent(uploadId)}`, { method: 'DELETE' });
  } catch {
    // expired or already gone: the server sweeps leftovers after 24 h anyway
    return;
  }
}
