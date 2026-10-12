// Rulers in design pixels along the top and left of the canvas. They follow zoom and
// scrolling, show the slide extent, the selection and the guides; dragging out of a ruler
// creates a guide line.
import { useCallback, useEffect, useLayoutEffect, useRef, type PointerEvent as ReactPointerEvent, type RefObject } from 'react';

export const RULER_SIZE = 20; // css px, keep in sync with editor.css

const STEPS = [1, 2, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000, 2000, 2500, 5000, 10000];

/** Major tick distance (design px) so that labels are at least ~64 screen px apart. */
export function rulerStep(scale: number): number {
  return STEPS.find((s) => s * scale >= 64) ?? STEPS[STEPS.length - 1]!;
}

/** Minor tick distance: a tenth, fifth or half of the major step, at least 5 screen px. */
export function rulerMinor(major: number, scale: number): number {
  for (const d of [10, 5, 2]) {
    const m = major / d;
    if (Number.isInteger(m) && m * scale >= 5) return m;
  }
  return major;
}

function cssVar(name: string, fallback: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

export function Ruler({
  axis,
  outerRef,
  slideRef,
  scale,
  size,
  range,
  marks,
  onGuideStart,
  rulerRef,
}: {
  /** 'x' = horizontal ruler above the canvas, 'y' = vertical ruler on the left. */
  axis: 'x' | 'y';
  outerRef: RefObject<HTMLDivElement | null>;
  slideRef: RefObject<HTMLDivElement | null>;
  scale: number;
  /** Slide size along this axis (design px). */
  size: number;
  /** Selection extent along this axis (design px). */
  range: [number, number] | null;
  /** Guide positions along this axis (design px). */
  marks: number[];
  onGuideStart: (e: ReactPointerEvent) => void;
  rulerRef: RefObject<HTMLCanvasElement | null>;
}) {
  const frame = useRef(0);

  const draw = useCallback(() => {
    const c = rulerRef.current;
    const slide = slideRef.current;
    if (!c || !slide) return;
    const ctx = c.getContext('2d');
    if (!ctx) return;
    const dpr = window.devicePixelRatio || 1;
    const w = c.clientWidth;
    const h = c.clientHeight;
    if (c.width !== Math.round(w * dpr) || c.height !== Math.round(h * dpr)) {
      c.width = Math.round(w * dpr);
      c.height = Math.round(h * dpr);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const len = axis === 'x' ? w : h;
    const thick = axis === 'x' ? h : w;
    const cr = c.getBoundingClientRect();
    const sr = slide.getBoundingClientRect();
    const origin = axis === 'x' ? sr.left - cr.left : sr.top - cr.top;
    const at = (v: number): number => origin + v * scale; // design → ruler px
    // Rect along/across the ruler (across 0 = far side, thick = the canvas side).
    const rect = (a: number, b: number, from: number, to: number): void => {
      if (axis === 'x') ctx.fillRect(a, from, b - a, to - from);
      else ctx.fillRect(from, a, to - from, b - a);
    };
    const tick = (p: number, length: number): void => {
      const x = Math.round(p) + 0.5;
      ctx.beginPath();
      if (axis === 'x') {
        ctx.moveTo(x, thick - length);
        ctx.lineTo(x, thick);
      } else {
        ctx.moveTo(thick - length, x);
        ctx.lineTo(thick, x);
      }
      ctx.stroke();
    };

    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = cssVar('--os-bg', '#201c18');
    ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = cssVar('--os-surface-2', '#332d26');
    rect(at(0), at(size), 0, thick);
    if (range) {
      ctx.fillStyle = cssVar('--os-primary-soft', 'rgba(255,138,0,0.3)');
      rect(at(range[0]), at(range[1]), 0, thick);
    }

    const major = rulerStep(scale);
    const minor = rulerMinor(major, scale);
    const first = Math.floor(-origin / scale / minor) * minor;
    const last = (len - origin) / scale;
    ctx.strokeStyle = cssVar('--os-text-faint', '#746b60');
    ctx.lineWidth = 1;
    ctx.fillStyle = cssVar('--os-text-muted', '#a99e90');
    ctx.font = '9px system-ui, sans-serif';
    ctx.textBaseline = 'alphabetic';
    for (let v = first; v <= last; v += minor) {
      const p = at(v);
      const isMajor = Math.abs(v / major - Math.round(v / major)) < 1e-6;
      const isHalf = !isMajor && Math.abs((2 * v) / major - Math.round((2 * v) / major)) < 1e-6;
      tick(p, isMajor ? thick : isHalf ? thick * 0.4 : thick * 0.22);
      if (!isMajor) continue;
      const label = String(Math.round(v));
      if (axis === 'x') {
        ctx.fillText(label, p + 3, 9);
      } else {
        ctx.save();
        ctx.translate(9, p + 3 + ctx.measureText(label).width);
        ctx.rotate(-Math.PI / 2);
        ctx.fillText(label, 0, 0);
        ctx.restore();
      }
    }

    ctx.fillStyle = cssVar('--os-accent', '#ff9f40');
    for (const m of marks) rect(at(m) - 1, at(m) + 1, thick * 0.45, thick);
    ctx.fillStyle = cssVar('--os-border', '#453e35');
    rect(0, len, thick - 1, thick);
  }, [axis, rulerRef, slideRef, scale, size, range, marks]);

  useLayoutEffect(() => draw(), [draw]);

  // Follow scrolling and size changes (also when the slide is re-centered).
  useEffect(() => {
    const outer = outerRef.current;
    const c = rulerRef.current;
    if (!outer || !c) return;
    const schedule = (): void => {
      cancelAnimationFrame(frame.current);
      frame.current = requestAnimationFrame(draw);
    };
    outer.addEventListener('scroll', schedule, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(c);
    ro.observe(outer);
    if (slideRef.current) ro.observe(slideRef.current);
    return () => {
      cancelAnimationFrame(frame.current);
      outer.removeEventListener('scroll', schedule);
      ro.disconnect();
    };
  }, [draw, outerRef, rulerRef, slideRef]);

  return (
    <canvas
      ref={rulerRef}
      className={`ruler ruler-${axis}`}
      aria-hidden="true"
      onPointerDown={(e) => {
        if (e.button === 0) onGuideStart(e);
      }}
    />
  );
}
