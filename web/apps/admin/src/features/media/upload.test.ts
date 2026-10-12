// @vitest-environment node
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../lib/api';
import { uploadFile } from './upload';

interface Req {
  method: string;
  url: string;
  range: string | null;
  size: number;
}

type Hook = (
  req: Req,
  n: number,
  setReceived: (v: number) => void,
) => Response | 'network' | 'lost' | undefined;

const json = (status: number, body: unknown): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const fail = (status: number, code: string, params: Record<string, unknown> = {}): Response =>
  json(status, { error: { code, message: code, params } });

/** A fake server implementing the resumable upload protocol of internal/server/media/uploads.go. */
function fakeServer(opts: { chunkSize?: number; received?: number; hook?: Hook } = {}) {
  const reqs: Req[] = [];
  let received = opts.received ?? 0;
  const asset = { id: 'asset-1', name: 'clip.mp4', kind: 'video' };
  const handle = (req: Req, init: RequestInit): Response => {
    if (req.method === 'POST') {
      return json(200, {
        uploadId: 'up-1',
        received,
        chunkSize: opts.chunkSize ?? 16 << 20,
        filename: JSON.parse(String(init.body)).filename,
      });
    }
    if (req.method === 'DELETE') return new Response(null, { status: 204 });
    const m = /^bytes (\*|(\d+)-(\d+))\/(\d+)$/.exec(req.range ?? '');
    if (req.method !== 'PUT' || !m) return fail(400, 'bad_range');
    const total = Number(m[4]);
    if (m[1] === '*')
      return received === total
        ? json(200, { uploadId: 'up-1', received, complete: true, asset })
        : json(200, { uploadId: 'up-1', received, complete: false });
    const start = Number(m[2]);
    const end = Number(m[3]);
    if (start !== received) return fail(409, 'upload_offset', { received });
    if (end - start + 1 !== req.size) return fail(400, 'bad_range');
    received = end + 1;
    return received === total
      ? json(200, { uploadId: 'up-1', received, complete: true, asset })
      : json(200, { uploadId: 'up-1', received, complete: false });
  };
  const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
    const headers = (init.headers ?? {}) as Record<string, string>;
    const req: Req = {
      method: init.method ?? 'GET',
      url,
      range: headers['Content-Range'] ?? null,
      size: init.body instanceof Blob ? init.body.size : 0,
    };
    reqs.push(req);
    if (init.signal?.aborted) throw new DOMException('aborted', 'AbortError');
    const hooked = opts.hook?.(req, reqs.length, (v) => (received = v));
    if (hooked === 'network') throw new TypeError('Failed to fetch');
    if (hooked === 'lost') {
      handle(req, init); // the server processed it, the answer never arrived
      throw new TypeError('Failed to fetch');
    }
    return hooked ?? handle(req, init);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { fetchMock, reqs, received: () => received };
}

const file = (n: number): Blob => new Blob([new Uint8Array(n).map((_, i) => i % 251)]);
const ranges = (reqs: Req[]): (string | null)[] => reqs.filter((r) => r.method === 'PUT').map((r) => r.range);
const noDelay = { retryDelayMs: () => 0 };

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('uploadFile', () => {
  it('sends the file in chunks with consecutive Content-Range offsets', async () => {
    const srv = fakeServer();
    const progress: number[] = [];
    const started: string[] = [];
    const r = await uploadFile({
      file: file(25),
      filename: 'clip.mp4',
      folderId: 'f1',
      chunkSize: 10,
      onProgress: (n) => progress.push(n),
      onStart: (id) => started.push(id),
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(r.uploadId).toBe('up-1');
    expect(started).toEqual(['up-1']);
    expect(srv.reqs[0]).toMatchObject({ method: 'POST', url: '/api/v1/media/uploads' });
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/25', 'bytes 10-19/25', 'bytes 20-24/25']);
    expect(srv.reqs.filter((q) => q.method === 'PUT').map((q) => q.size)).toEqual([10, 10, 5]);
    expect(progress).toEqual([0, 10, 20, 25]);
  });

  it('announces name, size, folder and the asset to replace', async () => {
    const srv = fakeServer();
    await uploadFile({ file: file(5), filename: 'new.png', folderId: null, replaceAssetId: 'asset-9' });
    const body = JSON.parse(String(srv.fetchMock.mock.calls[0]![1]!.body));
    expect(body).toEqual({ filename: 'new.png', size: 5, folderId: null, replaceAssetId: 'asset-9' });
  });

  it('never sends more than the server chunk limit', async () => {
    const srv = fakeServer({ chunkSize: 4 });
    await uploadFile({ file: file(10), filename: 'a.jpg', folderId: null, chunkSize: 100 });
    expect(ranges(srv.reqs)).toEqual(['bytes 0-3/10', 'bytes 4-7/10', 'bytes 8-9/10']);
  });

  it('continues at the offset the server reports with upload_offset', async () => {
    // Meanwhile the server got 20 bytes (e.g. a second tab resumed the same upload).
    const srv = fakeServer({
      hook: (req, _n, setReceived) => {
        if (req.method === 'PUT' && req.range === 'bytes 10-19/25') setReceived(20);
        return undefined;
      },
    });
    const progress: number[] = [];
    const r = await uploadFile({
      file: file(25),
      filename: 'clip.mp4',
      folderId: null,
      chunkSize: 10,
      onProgress: (n) => progress.push(n),
      ...noDelay,
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/25', 'bytes 10-19/25', 'bytes 20-24/25']);
    expect(progress).toContain(20);
  });

  it('resumes after a network error at the offset the server confirms', async () => {
    const retries: number[] = [];
    const srv = fakeServer({
      hook: (req, n) =>
        req.method === 'PUT' && req.range === 'bytes 10-19/30' && n === 3 ? 'network' : undefined,
    });
    const r = await uploadFile({
      file: file(30),
      filename: 'clip.mp4',
      folderId: null,
      chunkSize: 10,
      onRetry: (a) => retries.push(a),
      ...noDelay,
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(retries).toEqual([1]);
    // failed chunk → status query → same chunk again → rest
    expect(ranges(srv.reqs)).toEqual([
      'bytes 0-9/30',
      'bytes 10-19/30',
      'bytes */30',
      'bytes 10-19/30',
      'bytes 20-29/30',
    ]);
  });

  it('does not send a chunk twice when only its answer was lost', async () => {
    const srv = fakeServer({ hook: (req, n) => (req.method === 'PUT' && n === 3 ? 'lost' : undefined) });
    const r = await uploadFile({
      file: file(30),
      filename: 'clip.mp4',
      folderId: null,
      chunkSize: 10,
      ...noDelay,
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/30', 'bytes 10-19/30', 'bytes */30', 'bytes 20-29/30']);
  });

  it('finishes via the status query when the answer of the last chunk was lost', async () => {
    const srv = fakeServer({ hook: (req) => (req.range === 'bytes 10-14/15' ? 'lost' : undefined) });
    const r = await uploadFile({
      file: file(15),
      filename: 'clip.mp4',
      folderId: null,
      chunkSize: 10,
      ...noDelay,
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/15', 'bytes 10-14/15', 'bytes */15']);
  });

  it('retries 5xx answers and resumes from the server offset', async () => {
    let failures = 2;
    const srv = fakeServer({
      hook: (req) => {
        if (req.method === 'PUT' && req.range === 'bytes 0-9/20' && failures > 0) {
          failures--;
          return fail(503, 'unavailable');
        }
        return undefined;
      },
    });
    await uploadFile({ file: file(20), filename: 'a.mp3', folderId: null, chunkSize: 10, ...noDelay });
    expect(ranges(srv.reqs)).toEqual([
      'bytes 0-9/20',
      'bytes */20',
      'bytes 0-9/20',
      'bytes */20',
      'bytes 0-9/20',
      'bytes 10-19/20',
    ]);
    expect(srv.received()).toBe(20);
  });

  it('resumes an earlier upload by id without starting a new one', async () => {
    const srv = fakeServer({ received: 10 });
    const progress: number[] = [];
    const r = await uploadFile({
      file: file(25),
      filename: 'clip.mp4',
      folderId: null,
      uploadId: 'up-1',
      chunkSize: 10,
      onProgress: (n) => progress.push(n),
    });
    expect(r.asset?.id).toBe('asset-1');
    expect(srv.reqs.some((q) => q.method === 'POST')).toBe(false);
    expect(ranges(srv.reqs)).toEqual(['bytes */25', 'bytes 10-19/25', 'bytes 20-24/25']);
    expect(progress[0]).toBe(10);
  });

  it('does not retry definitive errors such as disk_full', async () => {
    const srv = fakeServer({
      hook: (req) => (req.method === 'PUT' ? fail(507, 'disk_full', { needed: 100, free: 5 }) : undefined),
    });
    const err = await uploadFile({
      file: file(20),
      filename: 'a.mp4',
      folderId: null,
      chunkSize: 10,
      ...noDelay,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('disk_full');
    expect((err as ApiError).params).toEqual({ needed: 100, free: 5 });
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/20']);
  });

  it('gives up after the configured number of consecutive failures', async () => {
    const srv = fakeServer({ hook: (req) => (req.method === 'PUT' ? 'network' : undefined) });
    const err = await uploadFile({
      file: file(20),
      filename: 'a.mp4',
      folderId: null,
      chunkSize: 10,
      maxRetries: 2,
      ...noDelay,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('network');
    // 3 chunk attempts, 2 status queries in between
    expect(ranges(srv.reqs)).toEqual([
      'bytes 0-9/20',
      'bytes */20',
      'bytes 0-9/20',
      'bytes */20',
      'bytes 0-9/20',
    ]);
  });

  it('stops when aborted', async () => {
    const ctrl = new AbortController();
    const srv = fakeServer({
      hook: (req) => {
        if (req.range === 'bytes 0-9/30') ctrl.abort();
        return undefined;
      },
    });
    const err = await uploadFile({
      file: file(30),
      filename: 'a.mp4',
      folderId: null,
      chunkSize: 10,
      signal: ctrl.signal,
    }).catch((e: unknown) => e);
    expect((err as Error).name).toBe('AbortError');
    expect(ranges(srv.reqs)).toEqual(['bytes 0-9/30']);
  });

  it('refuses an empty file without contacting the server', async () => {
    const srv = fakeServer();
    const err = await uploadFile({ file: file(0), filename: 'empty.png', folderId: null }).catch(
      (e: unknown) => e,
    );
    expect((err as ApiError).code).toBe('empty_file');
    expect(srv.fetchMock).not.toHaveBeenCalled();
  });
});
