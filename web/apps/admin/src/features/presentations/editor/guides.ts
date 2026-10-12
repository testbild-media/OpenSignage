// Guide lines and snapping. Guides are an editing aid, not part of the document contract:
// they are kept per presentation in this browser (localStorage), like the zoom or grid.
import type { Slide } from '@opensignage/contracts';

/** 'x' = vertical line at a fixed x, 'y' = horizontal line at a fixed y (design px). */
export type GuideAxis = 'x' | 'y';

export interface Guides {
  x: number[];
  y: number[];
}

const KEY = 'os.editor.guides.';

function numbers(v: unknown): number[] {
  return Array.isArray(v) ? v.filter((n): n is number => typeof n === 'number' && Number.isFinite(n)) : [];
}

export function loadGuides(presentationId: string): Guides {
  try {
    const raw = localStorage.getItem(KEY + presentationId);
    if (!raw) return { x: [], y: [] };
    const v = JSON.parse(raw) as Partial<Record<GuideAxis, unknown>>;
    return { x: numbers(v.x), y: numbers(v.y) };
  } catch {
    return { x: [], y: [] };
  }
}

export function saveGuides(presentationId: string, g: Guides): void {
  try {
    if (g.x.length === 0 && g.y.length === 0) localStorage.removeItem(KEY + presentationId);
    else localStorage.setItem(KEY + presentationId, JSON.stringify(g));
  } catch {
    // storage unavailable: the guides last until the editor is closed
  }
}

/**
 * Final position of a dragged guide: whole design pixels inside the slide, or null when it
 * was dropped onto a ruler or off the slide (= the guide is removed).
 */
export function dropGuide(pos: number, size: number, overRuler: boolean): number | null {
  if (overRuler || !Number.isFinite(pos) || pos < 0 || pos > size) return null;
  return Math.round(pos);
}

/** Snap targets: slide edges and center, edges and centers of other elements, guides. */
export function snapCandidates(slide: Slide, w: number, h: number, exclude: Set<string>, guides: Guides): { xs: number[]; ys: number[] } {
  const xs = [0, w / 2, w, ...guides.x];
  const ys = [0, h / 2, h, ...guides.y];
  for (const e of slide.elements) {
    if (exclude.has(e.id) || e.hidden) continue;
    xs.push(e.x, e.x + e.w / 2, e.x + e.w);
    ys.push(e.y, e.y + e.h / 2, e.y + e.h);
  }
  return { xs, ys };
}

/** The smallest correction that moves one of `values` onto one of `targets` (within tol). */
export function nearest(values: number[], targets: number[], tol: number): { delta: number; at: number } | null {
  let best: { delta: number; at: number } | null = null;
  for (const v of values) {
    for (const t of targets) {
      const d = t - v;
      if (Math.abs(d) <= tol && (!best || Math.abs(d) < Math.abs(best.delta))) best = { delta: d, at: t };
    }
  }
  return best;
}
