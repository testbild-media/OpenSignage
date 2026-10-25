import { describe, expect, it } from 'vitest';
import { analyzeWordmark, wordmarkBox, wordmarkOf } from './wordmark';

// Builds an alpha function from column ink ranges [x0, x1, top, bottom].
function image(w: number, h: number, inks: [number, number, number, number][]): (x: number, y: number) => number {
  return (x, y) => (inks.some(([x0, x1, t, b]) => x >= x0 && x <= x1 && y >= t && y <= b) ? 255 : 0);
}

describe('wordmark analysis', () => {
  it('finds the baseline and the tallest standing letter of a logo like "Red Bull" (® ignored)', () => {
    // 45 px high: R and B 2–43, round e and u overshoot to 44, l and d 0–42 (flat bottom at
    // 42 vs 43 is anti-aliasing), ® at the top right 0–7, wide capitals Z E R O 19–43.
    const a = image(340, 45, [
      [0, 56, 2, 43], [60, 88, 0, 43], [100, 131, 2, 42], [134, 160, 18, 44], [165, 172, 0, 42], [179, 186, 0, 42],
      [192, 199, 0, 7], [212, 230, 19, 43], [248, 261, 19, 43], [280, 296, 19, 43], [312, 338, 18, 43],
    ]);
    expect(analyzeWordmark(a, 340, 45)).toEqual({ top: 0, baseline: 43, height: 45 });
  });

  it('keeps a descender below the baseline and a mark above the letters out of the letter height', () => {
    // Letters 10–39, a descender column down to 59, an accent 0–5 above letters (same
    // columns, separated by a gap like in "Café").
    const a = image(200, 60, [[0, 60, 10, 39], [70, 90, 20, 59], [100, 190, 18, 39], [120, 130, 0, 5]]);
    const m = analyzeWordmark(a, 200, 60)!;
    expect(m).toEqual({ top: 10, baseline: 39, height: 60 });
    // Letters (30 rows) = 0.75 em → the image is 1.5 em high and reaches 0.5 em below the baseline.
    expect(wordmarkBox(m, 0.75)).toEqual({ height: 1.5, offset: -0.5 });
  });

  it('treats an opaque image as one block of letters', () => {
    const m = analyzeWordmark(() => 255, 100, 40)!;
    expect(m).toEqual({ top: 0, baseline: 39, height: 40 });
    expect(wordmarkBox(m, 0.75)).toEqual({ height: 0.75, offset: 0 });
  });

  it('counts a pixel as letter from half coverage on (anti-aliased edge rows do not)', () => {
    // Letters 5–34 fully covered, plus a lighter anti-aliased row 35 (alpha 100) below.
    const a = (x: number, y: number): number => (x >= 10 && x <= 60 ? (y >= 5 && y <= 34 ? 255 : y === 35 ? 100 : 0) : 0);
    expect(analyzeWordmark(a, 80, 40)).toEqual({ top: 5, baseline: 34, height: 40 });
  });

  it('measures every image itself, also two versions of a medium under one address', () => {
    // A replaced medium keeps its address: the old version (letters 10–39 in 60 rows) must not
    // lend its result to the new one (letters 0–39 in 40 rows).
    const src = '/api/v1/media/a/file?variant=display';
    const old = { currentSrc: src, src } as HTMLImageElement;
    const fresh = { currentSrc: src, src } as HTMLImageElement;
    let reads = 0;
    const reader = (pix: [number, number, number, number][], w: number, h: number) => () => {
      reads++;
      return { alpha: image(w, h, pix), w, h };
    };
    expect(wordmarkOf(old, reader([[0, 99, 10, 39]], 100, 60))).toEqual({ top: 10, baseline: 39, height: 60 });
    expect(wordmarkOf(fresh, reader([[0, 99, 0, 39]], 100, 40))).toEqual({ top: 0, baseline: 39, height: 40 });
    // The same image is read once.
    expect(wordmarkOf(old, reader([[0, 99, 0, 39]], 100, 40))).toEqual({ top: 10, baseline: 39, height: 60 });
    expect(reads).toBe(2);
  });

  it('gives up on empty or degenerate images', () => {
    expect(analyzeWordmark(() => 0, 50, 50)).toBeNull();
    expect(analyzeWordmark(image(50, 50, [[0, 49, 49, 49]]), 50, 50)).toBeNull();
  });
});
