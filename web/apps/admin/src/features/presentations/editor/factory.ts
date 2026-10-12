// Default elements for the Insert tab. Sizes are relative to the design resolution so a
// new element looks right on 1280×720 as well as on 3840×2160.
import type { Presentation, Slide, SlideElement, TextStyle } from '@opensignage/contracts';
import { newId } from './store';

export type InsertKind =
  | 'text'
  | 'image'
  | 'video'
  | 'audio'
  | 'rect'
  | 'roundrect'
  | 'ellipse'
  | 'line'
  | 'arrow'
  | 'productList'
  | 'product'
  | 'clock'
  | 'ticker'
  | 'logo';

export const DEFAULT_FONT = 'Montserrat';

export function baseStyle(size: number, overrides: Partial<TextStyle> = {}): TextStyle {
  return {
    font: DEFAULT_FONT,
    size,
    color: '#ffffff',
    bold: false,
    italic: false,
    underline: false,
    strike: false,
    letterSpacing: 0,
    lineHeight: 1.2,
    align: 'left',
    ...overrides,
  };
}

function box(doc: Presentation, wf: number, hf: number): { x: number; y: number; w: number; h: number } {
  const w = Math.round(doc.width * wf);
  const h = Math.round(doc.height * hf);
  return { x: Math.round((doc.width - w) / 2), y: Math.round((doc.height - h) / 2), w, h };
}

const common = { rotation: 0, opacity: 1, locked: false, hidden: false, pinned: false, name: '' };

/** Creates a new element; asset-based kinds need an assetId (from the media picker). */
export function createElement(kind: InsertKind, doc: Presentation, opts: { assetId?: string; productId?: string; text?: string } = {}): SlideElement {
  const id = newId();
  const u = doc.height / 1080; // unit relative to a 1080 px tall design
  switch (kind) {
    case 'text':
      return {
        ...common,
        id,
        type: 'text',
        ...box(doc, 0.5, 0.15),
        style: baseStyle(Math.round(64 * u)),
        valign: 'middle',
        padding: [0, 0, 0, 0],
        doc: { paragraphs: [{ runs: [{ text: opts.text ?? 'Text' }] }] },
      };
    case 'image':
    case 'logo':
      return {
        ...common,
        id,
        type: 'image',
        name: kind === 'logo' ? 'Logo' : '',
        pinned: kind === 'logo',
        ...(kind === 'logo' ? { x: Math.round(doc.width - 340 * u), y: Math.round(30 * u), w: Math.round(300 * u), h: Math.round(150 * u) } : box(doc, 0.4, 0.4)),
        assetId: opts.assetId ?? '',
        fit: 'contain',
      };
    case 'video':
      return { ...common, id, type: 'video', ...box(doc, 0.5, 0.5), assetId: opts.assetId ?? '', fit: 'contain', loop: true, muted: true };
    case 'audio':
      return { ...common, id, type: 'audio', x: Math.round(40 * u), y: Math.round(40 * u), w: Math.round(80 * u), h: Math.round(80 * u), assetId: opts.assetId ?? '', loop: true, volume: 0.8 };
    case 'rect':
    case 'roundrect':
    case 'ellipse':
      return {
        ...common,
        id,
        type: 'shape',
        ...box(doc, 0.3, 0.3),
        shape: kind === 'ellipse' ? 'ellipse' : 'rect',
        fill: '#ff8a00',
        stroke: { color: '#ffffff', width: 0, dash: 'solid' },
        radius: kind === 'roundrect' ? Math.round(32 * u) : 0,
      };
    case 'line':
    case 'arrow':
      return {
        ...common,
        id,
        type: 'shape',
        ...box(doc, 0.4, 0.04),
        shape: kind,
        stroke: { color: '#ffffff', width: Math.max(1, Math.round(6 * u)), dash: 'solid' },
      };
    case 'productList':
      return {
        ...common,
        id,
        type: 'productList',
        x: Math.round(doc.width * 0.05),
        y: Math.round(doc.height * 0.15),
        w: Math.round(doc.width * 0.9),
        h: Math.round(doc.height * 0.75),
        source: { kind: 'all', ids: [] },
        columns: 2,
        fields: { name: true, quantity: true, description: true, price: true },
        categoryHeadings: true,
        subcategoryHeadings: true,
        scale: 1,
        rowGap: Math.round(8 * u),
        columnGap: Math.round(60 * u),
        priceAlign: 'right',
      };
    case 'product':
      return {
        ...common,
        id,
        type: 'product',
        ...box(doc, 0.4, 0.1),
        productId: opts.productId ?? '',
        fields: { name: true, quantity: true, description: false, price: true },
        scale: 1,
        priceAlign: 'right',
      };
    case 'clock':
      return {
        ...common,
        id,
        type: 'clock',
        x: Math.round(doc.width - 420 * u),
        y: Math.round(doc.height - 140 * u),
        w: Math.round(380 * u),
        h: Math.round(110 * u),
        format: 'HH:mm',
        date: 'none',
        style: baseStyle(Math.round(72 * u), { align: 'right', bold: true }),
        valign: 'middle',
      };
    case 'ticker':
      return {
        ...common,
        id,
        type: 'ticker',
        x: 0,
        y: Math.round(doc.height - 80 * u),
        w: doc.width,
        h: Math.round(80 * u),
        tickerSource: 'zone',
        text: '',
        style: baseStyle(Math.round(44 * u)),
        fill: '#201c18',
        speed: Math.round(140 * u),
        direction: 'left',
      };
  }
}

export function createSlide(durationMs: number): Slide {
  return {
    id: newId(),
    name: '',
    hidden: false,
    duration: { mode: 'fixed', ms: durationMs },
    background: { type: 'color', color: '#000000' },
    transition: { type: 'fade', ms: 600 },
    elements: [],
  };
}

export const RESOLUTION_PRESETS: { label: string; w: number; h: number }[] = [
  { label: 'Full HD 16:9', w: 1920, h: 1080 },
  { label: 'HD 16:9', w: 1280, h: 720 },
  { label: '4K 16:9', w: 3840, h: 2160 },
  { label: 'HD ready 16:9', w: 1366, h: 768 },
  { label: 'Full HD 9:16', w: 1080, h: 1920 },
  { label: '4:3', w: 1600, h: 1200 },
  { label: '3:4', w: 1200, h: 1600 },
  { label: '21:9', w: 2560, h: 1080 },
  { label: 'LED 32:9', w: 2560, h: 720 },
];

export const ASPECTS: { label: string; w: number; h: number }[] = [
  { label: '16:9', w: 16, h: 9 },
  { label: '9:16', w: 9, h: 16 },
  { label: '4:3', w: 4, h: 3 },
  { label: '3:4', w: 3, h: 4 },
  { label: '21:9', w: 21, h: 9 },
];

/** Suggests a resolution for an aspect ratio (long side 1920). */
export function resolutionForAspect(aw: number, ah: number): { w: number; h: number } {
  if (aw >= ah) return { w: 1920, h: Math.round((1920 * ah) / aw) };
  return { w: Math.round((1920 * aw) / ah), h: 1920 };
}

export function aspectLabel(w: number, h: number): string {
  const g = (a: number, b: number): number => (b === 0 ? a : g(b, a % b));
  const d = g(w, h);
  const known = ASPECTS.find((a) => Math.abs(a.w / a.h - w / h) < 0.01);
  return known ? known.label : `${w / d}:${h / d}`;
}
