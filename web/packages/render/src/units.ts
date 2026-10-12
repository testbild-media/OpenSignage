import type { Marks, TextStyle } from '@opensignage/contracts';

/** A design-pixel length as CSS: scales with the stage factor --s (set on the canvas). */
export function px(n: number): string {
  if (!Number.isFinite(n) || n === 0) return '0px';
  return `calc(var(--s) * ${round(n)}px)`;
}

/** Font-size aware length (auto-shrink multiplies with --fit). */
export function fpx(n: number): string {
  return `calc(var(--s) * var(--fit, 1) * ${round(n)}px)`;
}

export function round(n: number): number {
  return Math.round(n * 1000) / 1000;
}

/** Converts #rrggbbaa to rgba() (CSS supports 8-digit hex, but older Chromium builds on
 * some Pi images had issues; rgba is universally safe). */
export function color(c: string | undefined, fallback = 'transparent'): string {
  if (!c) return fallback;
  if (/^#[0-9a-fA-F]{8}$/.test(c)) {
    const r = parseInt(c.slice(1, 3), 16);
    const g = parseInt(c.slice(3, 5), 16);
    const b = parseInt(c.slice(5, 7), 16);
    const a = parseInt(c.slice(7, 9), 16) / 255;
    return `rgba(${r},${g},${b},${round(a)})`;
  }
  return c;
}

export const FALLBACK_FONT = 'Noto Sans';

export function fontFamily(name: string): string {
  return `"${name.replace(/"/g, '')}", "${FALLBACK_FONT}", sans-serif`;
}

/** Applies a complete text style to an element (size scaled by factor). */
export function applyTextStyle(el: HTMLElement, s: TextStyle, factor = 1): void {
  const st = el.style;
  st.fontFamily = fontFamily(s.font);
  st.fontSize = fpx(s.size * factor);
  st.color = color(s.color, '#ffffff');
  st.fontWeight = s.bold ? '700' : '400';
  st.fontStyle = s.italic ? 'italic' : 'normal';
  st.textDecorationLine = [s.underline ? 'underline' : '', s.strike ? 'line-through' : ''].join(' ').trim() || 'none';
  st.letterSpacing = s.letterSpacing ? fpx(s.letterSpacing * factor) : 'normal';
  st.lineHeight = String(s.lineHeight || 1.2);
  st.textAlign = s.align;
  st.textTransform = s.uppercase ? 'uppercase' : 'none';
  if (s.highlight) st.backgroundColor = color(s.highlight);
  applyEffects(st, s, factor);
}

function applyEffects(st: CSSStyleDeclaration, s: TextStyle, factor: number): void {
  if (s.outline && s.outline.width > 0) {
    st.setProperty('-webkit-text-stroke', `${fpx(s.outline.width * factor)} ${color(s.outline.color)}`);
    st.setProperty('paint-order', 'stroke fill');
  }
  if (s.shadow) {
    // Hard shadow only: blur radius is always 0 by design.
    st.textShadow = `${fpx(s.shadow.dx * factor)} ${fpx(s.shadow.dy * factor)} 0 ${color(s.shadow.color)}`;
  }
}

/** Applies partial marks (rich text runs, product list overrides). */
export function applyMarks(el: HTMLElement, m: Marks | undefined, factor = 1): void {
  if (!m) return;
  const st = el.style;
  if (m.font !== undefined) st.fontFamily = fontFamily(m.font);
  if (m.size !== undefined) st.fontSize = fpx(m.size * factor);
  if (m.color !== undefined) st.color = color(m.color);
  if (m.bold !== undefined) st.fontWeight = m.bold ? '700' : '400';
  if (m.italic !== undefined) st.fontStyle = m.italic ? 'italic' : 'normal';
  if (m.underline !== undefined || m.strike !== undefined) {
    const parts: string[] = [];
    if (m.underline) parts.push('underline');
    if (m.strike) parts.push('line-through');
    st.textDecorationLine = parts.join(' ') || 'none';
  }
  if (m.highlight !== undefined) st.backgroundColor = color(m.highlight);
  if (m.letterSpacing !== undefined) st.letterSpacing = fpx(m.letterSpacing * factor);
}

/** Merges a style with partial marks into a full style. */
export function mergeStyle(base: TextStyle, m: Marks | undefined): TextStyle {
  if (!m) return base;
  return {
    ...base,
    font: m.font ?? base.font,
    size: m.size ?? base.size,
    color: m.color ?? base.color,
    bold: m.bold ?? base.bold,
    italic: m.italic ?? base.italic,
    underline: m.underline ?? base.underline,
    strike: m.strike ?? base.strike,
    highlight: m.highlight ?? base.highlight,
    letterSpacing: m.letterSpacing ?? base.letterSpacing,
  };
}
