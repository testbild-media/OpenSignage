// Upload queue outside React: routes remount on every path change, uploads must not.
// Two files upload at a time; every file has its own progress, retry and cancel.
import { useEffect, useRef, useSyncExternalStore } from 'react';
import { ApiError } from '../../lib/api';
import type { MediaAsset } from './model';
import { abortUpload, isAbort, uploadFile } from './upload';

export type UploadState = 'queued' | 'uploading' | 'retrying' | 'done' | 'error' | 'canceled';

export interface UploadItem {
  key: number;
  file: File;
  name: string;
  folderId: string | null;
  replaceAssetId?: string;
  state: UploadState;
  received: number;
  size: number;
  attempt: number;
  error: unknown;
  uploadId?: string;
  asset?: MediaAsset | null;
}

const CONCURRENCY = 2;
let items: UploadItem[] = [];
let nextKey = 1;
let running = 0;
const controllers = new Map<number, AbortController>();
const listeners = new Set<() => void>();
const doneListeners = new Set<(item: UploadItem) => void>();

const ACTIVE: UploadState[] = ['queued', 'uploading', 'retrying'];
export const isActive = (i: UploadItem): boolean => ACTIVE.includes(i.state);

function emit(): void {
  for (const l of listeners) l();
  syncUnloadGuard();
}

function patch(key: number, p: Partial<UploadItem>): void {
  items = items.map((i) => (i.key === key ? { ...i, ...p } : i));
  emit();
}

function find(key: number): UploadItem | undefined {
  return items.find((i) => i.key === key);
}

/** Adds files to the queue (folderId null = top level) and returns their queue keys. */
export function enqueueUploads(files: File[], folderId: string | null, replaceAssetId?: string): number[] {
  if (!files.length) return [];
  const added = files.map<UploadItem>((file) => ({
    key: nextKey++,
    file,
    name: file.name || 'file',
    folderId,
    replaceAssetId,
    state: 'queued',
    received: 0,
    size: file.size,
    attempt: 0,
    error: null,
  }));
  items = [...items, ...added];
  emit();
  pump();
  return added.map((i) => i.key);
}

function pump(): void {
  while (running < CONCURRENCY) {
    const next = items.find((i) => i.state === 'queued');
    if (!next) return;
    running++;
    patch(next.key, { state: 'uploading', error: null, attempt: 0 });
    void run(next.key).finally(() => {
      running--;
      pump();
    });
  }
}

async function run(key: number): Promise<void> {
  const item = find(key);
  if (!item) return;
  const ctrl = new AbortController();
  controllers.set(key, ctrl);
  try {
    const r = await uploadFile({
      file: item.file,
      filename: item.name,
      folderId: item.folderId,
      replaceAssetId: item.replaceAssetId,
      uploadId: item.uploadId,
      signal: ctrl.signal,
      onStart: (uploadId) => patch(key, { uploadId }),
      onProgress: (received) => patch(key, { received, state: 'uploading' }),
      onRetry: (attempt, error) => patch(key, { state: 'retrying', attempt, error }),
    });
    patch(key, { state: 'done', received: item.size, asset: r.asset, error: null, uploadId: undefined });
    const done = find(key);
    if (done) for (const l of doneListeners) l(done);
  } catch (e) {
    const cur = find(key);
    if (isAbort(e) || cur?.state === 'canceled') {
      if (cur?.uploadId) void abortUpload(cur.uploadId);
      patch(key, { state: 'canceled', uploadId: undefined });
    } else {
      // An upload the server no longer knows (expired, refused for good) starts over on retry.
      const gone = e instanceof ApiError && e.status >= 400 && e.status < 500 && e.code !== 'upload_offset';
      patch(key, { state: 'error', error: e, ...(gone ? { uploadId: undefined, received: 0 } : {}) });
    }
  } finally {
    controllers.delete(key);
  }
}

export function cancelUpload(key: number): void {
  const item = find(key);
  if (!item || !isActive(item)) return;
  const ctrl = controllers.get(key);
  if (ctrl) {
    patch(key, { state: 'canceled' });
    ctrl.abort();
  } else {
    patch(key, { state: 'canceled' });
  }
}

/** Retries a failed upload; it resumes at the offset the server has. */
export function retryUpload(key: number): void {
  const item = find(key);
  if (!item || (item.state !== 'error' && item.state !== 'canceled')) return;
  patch(key, { state: 'queued', error: null });
  pump();
}

export function clearFinished(): void {
  items = items.filter(isActive);
  emit();
}

export function removeUpload(key: number): void {
  const item = find(key);
  if (!item || isActive(item)) return;
  items = items.filter((i) => i.key !== key);
  emit();
}

function subscribe(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function useUploads(): UploadItem[] {
  return useSyncExternalStore(subscribe, () => items);
}

/** Calls fn whenever an upload finished (the asset list should show the new file). */
export function useUploadDone(fn: (item: UploadItem) => void): void {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    const l = (i: UploadItem): void => ref.current(i);
    doneListeners.add(l);
    return () => {
      doneListeners.delete(l);
    };
  }, []);
}

// Leaving the page loses the file handles: warn while uploads are running.
let guarded = false;
const onBeforeUnload = (e: BeforeUnloadEvent): void => {
  e.preventDefault();
};
function syncUnloadGuard(): void {
  if (typeof window === 'undefined') return;
  const need = items.some(isActive);
  if (need && !guarded) window.addEventListener('beforeunload', onBeforeUnload);
  if (!need && guarded) window.removeEventListener('beforeunload', onBeforeUnload);
  guarded = need;
}
