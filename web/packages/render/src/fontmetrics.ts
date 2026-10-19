// Height of the tall letters of a font (l, b, d, h, k and the capitals – the highest of them)
// as a share of its size. A brand logo shown instead of a product name is as high as these
// letters: the l of "Red Bull" in the logo must be as high as the l of "Cola" next to it.
// CSS has a unit for capital letters (cap) but none for ascenders, so the height is
// measured with a canvas once the font is loaded.
import { fontFamily } from './units';

const SAMPLE = 'bdhklH';
const cache = new Map<string, number>();

function spec(font: string, weight: string): string {
  return `${weight} 100px ${fontFamily(font)}`;
}

/** The ratio when the font is ready (null: not loaded yet, or no layout engine). */
export function ascenderRatio(font: string, weight: string): number | null {
  const key = `${font}|${weight}`;
  const hit = cache.get(key);
  if (hit !== undefined) return hit;
  if (typeof document === 'undefined' || !document.fonts) return null;
  try {
    if (!document.fonts.check(spec(font, weight))) return null;
    const ctx = document.createElement('canvas').getContext('2d');
    if (!ctx) return null;
    ctx.font = spec(font, weight);
    const r = ctx.measureText(SAMPLE).actualBoundingBoxAscent / 100;
    if (!(r > 0.3 && r < 1.5)) return null; // no real text metrics
    cache.set(key, r);
    return r;
  } catch {
    return null;
  }
}

/** Calls fn with the ratio at once when the font is ready, else after it has loaded. */
export function whenAscender(font: string, weight: string, fn: (ratio: number) => void): void {
  const r = ascenderRatio(font, weight);
  if (r !== null) {
    fn(r);
    return;
  }
  if (typeof document === 'undefined' || !document.fonts) return;
  document.fonts
    .load(spec(font, weight))
    .then(() => {
      const later = ascenderRatio(font, weight);
      if (later !== null) fn(later);
    })
    .catch(() => {});
}

// Where a strike-through bar belongs: through the middle of the small letters (x-height), as
// a distance from the top of the text box in em. The box of an inline text starts the font's
// ascent above the baseline, so the bar sits at ascent - x-height / 2. Measured like the
// ascender, once the font is loaded.
const strikeCache = new Map<string, number>();

export function strikeTop(font: string, weight: string): number | null {
  const key = `${font}|${weight}`;
  const hit = strikeCache.get(key);
  if (hit !== undefined) return hit;
  if (typeof document === 'undefined' || !document.fonts) return null;
  try {
    if (!document.fonts.check(spec(font, weight))) return null;
    const ctx = document.createElement('canvas').getContext('2d');
    if (!ctx) return null;
    ctx.font = spec(font, weight);
    const m = ctx.measureText('x');
    const ascent = m.fontBoundingBoxAscent / 100;
    const x = m.actualBoundingBoxAscent / 100;
    if (!(ascent > 0.4 && ascent < 2 && x > 0.2 && x < ascent)) return null; // no real text metrics
    const top = ascent - x / 2;
    strikeCache.set(key, top);
    return top;
  } catch {
    return null;
  }
}

/** Calls fn with strikeTop at once when the font is ready, else after it has loaded. */
export function whenStrike(font: string, weight: string, fn: (top: number) => void): void {
  const r = strikeTop(font, weight);
  if (r !== null) {
    fn(r);
    return;
  }
  if (typeof document === 'undefined' || !document.fonts) return;
  document.fonts
    .load(spec(font, weight))
    .then(() => {
      const later = strikeTop(font, weight);
      if (later !== null) fn(later);
    })
    .catch(() => {});
}

/** Tests only. */
export function resetFontMetrics(): void {
  cache.clear();
  strikeCache.clear();
}
