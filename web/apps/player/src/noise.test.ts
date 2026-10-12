// Plasma regeneration noise: random grey pixels (all levels, no structure), the 2D canvas
// when WebGL is missing, nothing left behind after stop.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createNoise, fillNoise } from './noise';

describe('fillNoise', () => {
  it('writes opaque grey pixels with evenly spread levels', () => {
    const px = new Uint32Array(256 * 256);
    fillNoise(px, 12345);
    const counts = new Array<number>(256).fill(0);
    let bad = 0;
    for (const p of px) {
      const r = p & 0xff;
      // opaque and grey (R = G = B)
      if (p >>> 24 !== 0xff || ((p >>> 8) & 0xff) !== r || ((p >>> 16) & 0xff) !== r) bad++;
      counts[r]!++;
    }
    expect(bad).toBe(0);
    // 65536 samples over 256 levels: 256 expected each; every level occurs, none dominates.
    expect(Math.min(...counts)).toBeGreaterThan(150);
    expect(Math.max(...counts)).toBeLessThan(370);
  });

  it('continues the sequence: the next frame differs', () => {
    const a = new Uint32Array(64);
    const b = new Uint32Array(64);
    const next = fillNoise(a, 7);
    fillNoise(b, next);
    expect(next).not.toBe(7);
    expect([...a]).not.toEqual([...b]);
  });

  it('survives a zero seed', () => {
    const px = new Uint32Array(16);
    fillNoise(px, 0);
    expect(new Set(px).size).toBeGreaterThan(1);
  });
});

describe('createNoise', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function fake2d(): { put: ReturnType<typeof vi.fn> } {
    const put = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (
      this: HTMLCanvasElement,
      kind: string,
    ) {
      if (kind !== '2d') return null; // no WebGL here
      return {
        createImageData: (w: number, h: number) => ({
          width: w,
          height: h,
          data: new Uint8ClampedArray(w * h * 4),
        }),
        putImageData: put,
      } as unknown as CanvasRenderingContext2D;
    } as typeof HTMLCanvasElement.prototype.getContext);
    return { put };
  }

  it('falls back to the 2D canvas at half resolution and draws at once', () => {
    const { put } = fake2d();
    const host = document.createElement('div');
    host.className = 'hidden';
    const n = createNoise(host);
    n.start();
    expect(n.running).toBe(true);
    expect(n.backend).toBe('2d');
    expect(host.classList.contains('hidden')).toBe(false);
    const canvas = host.querySelector('canvas')!;
    expect(canvas.width).toBe(Math.ceil(window.innerWidth / 2));
    expect(canvas.height).toBe(Math.ceil(window.innerHeight / 2));
    expect(put).toHaveBeenCalledTimes(1);
    const img = put.mock.calls[0]![0] as ImageData;
    expect(new Set(new Uint32Array(img.data.buffer)).size).toBeGreaterThan(100);

    n.stop();
    expect(n.running).toBe(false);
    expect(host.querySelector('canvas')).toBeNull();
    expect(host.classList.contains('hidden')).toBe(true);
  });

  it('start and stop are idempotent', () => {
    const { put } = fake2d();
    const host = document.createElement('div');
    const n = createNoise(host);
    n.start();
    n.start();
    expect(host.querySelectorAll('canvas')).toHaveLength(1);
    expect(put).toHaveBeenCalledTimes(1);
    n.stop();
    n.stop();
    expect(n.backend).toBe('none');
  });
});
