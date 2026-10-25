// Where the letters of a wordmark image (a brand logo shown instead of a product name) are:
// the row its letters stand on (baseline) and the top of the tallest standing letter. The
// logo is then scaled so that this letter height equals the tall letters of the item font
// and its baseline sits on the text baseline – the l of "Red Bull" as high as the l of
// "Cola", whatever space the image has below (overshoot, descenders) or above (®).

export interface Wordmark {
  /** Top row of the tallest letter standing on the baseline. */
  top: number;
  /** Bottom row of the letters (flat bottoms of l, d, R …). */
  baseline: number;
  /** Image height in pixels. */
  height: number;
}

// A pixel belongs to a letter from half coverage on: the visible edge of an anti-aliased
// letter is where it is half covered (lighter rows at the edges do not make it look taller).
const ALPHA = 128;

/**
 * Analyses the alpha channel (alpha(x, y) 0–255). Every column with ink gives its lowest ink
 * row and the top of the stroke that reaches down to it (a mark above with a gap – accent,
 * dot, ® – is not part of the stroke). The most frequent lowest row is the baseline (flat
 * letter bottoms share it, round letters overshoot it slightly, descenders are far fewer
 * columns); the strokes standing on it give the letter top. An opaque image (no
 * transparency) counts as one block of letters. null: nothing usable.
 */
export function analyzeWordmark(alpha: (x: number, y: number) => number, w: number, h: number): Wordmark | null {
  if (w < 1 || h < 2) return null;
  const tops: number[] = [];
  const bottoms: number[] = [];
  for (let x = 0; x < w; x++) {
    let bottom = h - 1;
    while (bottom >= 0 && alpha(x, bottom) <= ALPHA) bottom--;
    if (bottom < 0) continue;
    let top = bottom;
    while (top > 0 && alpha(x, top - 1) > ALPHA) top--;
    tops.push(top);
    bottoms.push(bottom);
  }
  if (tops.length < 2) return null;
  const count = new Map<number, number>();
  for (const b of bottoms) count.set(b, (count.get(b) ?? 0) + 1);
  let baseline = -1;
  let best = 0;
  for (const [b, n] of count) {
    if (n > best || (n === best && b > baseline)) {
      best = n;
      baseline = b;
    }
  }
  const tol = Math.max(1, Math.round(h * 0.04)); // overshoot of round letters
  let top = h;
  for (let i = 0; i < tops.length; i++) {
    if (Math.abs(bottoms[i]! - baseline) <= tol) top = Math.min(top, tops[i]!);
  }
  if (top >= baseline || baseline - top + 1 < h * 0.2) return null;
  return { top, baseline, height: h };
}

/**
 * Box of the image in em of the item font: height, and the vertical offset of its bottom
 * edge from the text baseline (negative = below), so that the letters are `ascender` em high.
 */
export function wordmarkBox(m: Wordmark, ascender: number): { height: number; offset: number } {
  const letters = m.baseline + 1 - m.top; // rows from the top of the tallest letter to the baseline
  const em = ascender / letters; // em per image row
  const round = (v: number): number => Math.round(v * 10000) / 10000;
  const offset = round(-(m.height - 1 - m.baseline) * em);
  return { height: round(m.height * em), offset: offset === 0 ? 0 : offset };
}

/** The alpha channel of a loaded image; null when it cannot be read (cross origin). */
export type AlphaReader = (img: HTMLImageElement) => { alpha: (x: number, y: number) => number; w: number; h: number } | null;

function readAlpha(img: HTMLImageElement): ReturnType<AlphaReader> {
  const w = img.naturalWidth;
  const h = img.naturalHeight;
  const ctx = w && h ? document.createElement('canvas').getContext('2d', { willReadFrequently: true }) : null;
  if (!ctx) return null;
  ctx.canvas.width = w;
  ctx.canvas.height = h;
  ctx.drawImage(img, 0, 0);
  const data = ctx.getImageData(0, 0, w, h).data;
  return { alpha: (x, y) => data[(y * w + x) * 4 + 3]!, w, h };
}

// One result per image element and source. Never per address alone: a replaced medium keeps
// its address, and a result of its previous version gave the new logo the wrong size.
const cache = new WeakMap<HTMLImageElement, { src: string; result: Wordmark | null }>();

/** Analyses a loaded image (same origin); null when it cannot be read (cross origin). */
export function wordmarkOf(img: HTMLImageElement, read: AlphaReader = readAlpha): Wordmark | null {
  const src = img.currentSrc || img.src;
  const hit = cache.get(img);
  if (hit && hit.src === src) return hit.result;
  let result: Wordmark | null = null;
  try {
    const a = read(img);
    result = a ? analyzeWordmark(a.alpha, a.w, a.h) : null;
  } catch {
    result = null;
  }
  cache.set(img, { src, result });
  return result;
}
