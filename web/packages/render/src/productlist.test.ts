// Product states on screen: SOLD_OUT greyed with a red bar (also over
// product images), UNAVAILABLE removed without an empty row, category headings never
// greyed. Complements render.test.ts (initial rendering and row patching).
import { beforeEach, describe, expect, it } from 'vitest';
import type { LiveProduct, LiveState, ProductListElement, ProductStatus } from '@opensignage/contracts';
import { renderElement } from './elements';
import { injectRenderCss } from './css';
import { LiveStore } from './live';
import { resetFontMetrics } from './fontmetrics';
import { base, testEnv } from './testing';

const list: ProductListElement = {
  ...base('list', { w: 800, h: 600 }),
  type: 'productList',
  source: { kind: 'all', ids: [] },
  columns: 1,
  fields: { name: true, quantity: false, description: false, price: true },
  categoryHeadings: true,
  subcategoryHeadings: true,
  scale: 1,
  priceAlign: 'right',
};

function product(
  id: string,
  categoryId: string,
  sort: number,
  status: ProductStatus = 'AVAILABLE',
  patch: Partial<LiveProduct> = {},
): LiveProduct {
  return {
    id,
    name: id.toUpperCase(),
    description: '',
    categoryId,
    type: 'drink',
    price: { amount: 400 + sort, currency: 'EUR' },
    status,
    showImageInsteadOfName: false,
    sort,
    ...patch,
  };
}

function state(statuses: Record<string, ProductStatus> = {}): LiveState {
  const st = (id: string): ProductStatus => statuses[id] ?? 'AVAILABLE';
  return {
    zone: { id: 'z', name: 'Bar', state: 'OPEN' },
    ticker: {
      text: '',
      visible: false,
      template: {
        font: 'Montserrat',
        size: 40,
        color: '#fff',
        background: '#000',
        speed: 100,
        direction: 'left',
        height: 64,
        position: 'bottom',
      },
    },
    categories: [
      { id: 'beer', name: 'Beer', sort: 1 },
      { id: 'wine', name: 'Wine', sort: 2 },
    ],
    products: [
      product('pils', 'beer', 1, st('pils')),
      product('brand', 'beer', 2, st('brand'), { imageAssetId: 'brand-logo', showImageInsteadOfName: true }),
      product('weizen', 'beer', 3, st('weizen')),
      product('riesling', 'wine', 1, st('riesling')),
    ],
    serverTime: new Date().toISOString(),
  };
}

function rule(selector: string): CSSStyleRule {
  for (const sheet of document.styleSheets) {
    for (const r of sheet.cssRules) {
      if (r instanceof CSSStyleRule && r.selectorText === selector) return r;
    }
  }
  throw new Error(`no CSS rule ${selector}`);
}

let store: LiveStore;
let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  injectRenderCss(document); // the renderer's real stylesheet (once per document)
  store = new LiveStore();
  store.set(state());
  root = renderElement(list, testEnv(store)).el;
  document.body.appendChild(root);
});

const rowIds = (): string[] => [...root.querySelectorAll<HTMLElement>('.os-row')].map((r) => r.dataset.id!);
const row = (id: string): HTMLElement => root.querySelector<HTMLElement>(`.os-row[data-id="${id}"]`)!;
const headings = (): string[] => [...root.querySelectorAll('.os-pl-h')].map((h) => h.textContent ?? '');

describe('image instead of the name', () => {
  it('carries the item font, sits on the baseline, width by aspect ratio (capital height until measured)', () => {
    const name = row('brand').querySelector<HTMLElement>('.os-name')!;
    const text = row('pils').querySelector<HTMLElement>('.os-name')!;
    // The cell carries the item font, so line height and baseline equal a text row and the
    // CSS unit cap refers to that font.
    expect(name.style.fontSize).toBe(text.style.fontSize);
    expect(name.style.fontFamily).toBe(text.style.fontFamily);
    expect(name.style.lineHeight).toBe(text.style.lineHeight);
    const logo = name.querySelector<HTMLImageElement>('img.os-logo')!;
    expect(logo.style.height).toBe(''); // font not measurable (no layout engine): CSS cap
    const css = rule('.os-row .os-logo').cssText;
    expect(css).toMatch(/height: calc\((1cap|0?\.72em) \* var\(--logo-scale, ?1\)\)/); // fallback before measuring
    expect(css).toContain('vertical-align: baseline');
    expect(css).toContain('width: auto');
    expect(css).toContain('object-fit: contain');
  });
});

describe('image instead of the name, measured font', () => {
  it('is as high as the tall letters of the item font (l of the logo = l of the text)', async () => {
    // A layout engine: fonts are loaded, the canvas reports the ascent of "bdhklH".
    const fonts = { check: () => true, load: () => Promise.resolve([]) };
    Object.defineProperty(document, 'fonts', { value: fonts, configurable: true });
    const origGetContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = (() => ({ font: '', measureText: () => ({ actualBoundingBoxAscent: 74.2 }) })) as never;
    try {
      resetFontMetrics();
      const el = renderElement(list, testEnv(store)).el;
      document.body.appendChild(el);
      const logo = el.querySelector<HTMLImageElement>('.os-row[data-id="brand"] img.os-logo')!;
      expect(logo.style.height).toBe('0.742em');
      // Logo size of the product (brand guidelines): 150 % of the automatic size.
      const s = state();
      s.products = s.products.map((p) => (p.id === 'brand' ? { ...p, imageScale: 150 } : p));
      store.set(s);
      const bigger = el.querySelector<HTMLImageElement>('.os-row[data-id="brand"] img.os-logo')!;
      expect(bigger.style.height).toBe('1.113em');
      expect(bigger.style.getPropertyValue('--logo-scale')).toBe('1.5'); // CSS fallback before measuring
    } finally {
      HTMLCanvasElement.prototype.getContext = origGetContext;
      Reflect.deleteProperty(document, 'fonts');
      resetFontMetrics();
    }
  });
});

describe('SOLD_OUT', () => {
  it('greys every part of the row and strikes through the main line only, product images included', () => {
    store.set(state({ brand: 'SOLD_OUT', pils: 'SOLD_OUT' }));
    const brand = row('brand');
    expect(brand.classList.contains('os-soldout')).toBe(true);
    const logo = brand.querySelector<HTMLImageElement>('.os-name > img.os-logo')!;
    expect(logo.getAttribute('src')).toBe('/media/brand-logo/display');
    // Every child of the row (image container, quantity, price) is greyed.
    for (const child of brand.children) expect(getComputedStyle(child).opacity).toBe('0.42');
    expect(getComputedStyle(row('weizen').children[0]!).opacity).not.toBe('0.42');
    // The bar is the row's ::after, a grid item in the FIRST row spanning all columns:
    // name (or image), quantity and price are struck through in the middle of their line,
    // the description below stays greyed but not struck.
    const bar = rule('.os-row.os-soldout::after').style;
    expect([bar.gridRow, bar.gridColumn, bar.alignSelf]).toEqual(['1', '1 / -1', 'center']);
    expect(bar.position).not.toBe('absolute');
    expect(rule('.os-row .os-desc').style.gridRow).toBe('2');
    for (const cls of ['os-name', 'os-qty', 'os-price']) expect(rule(`.os-row>.${cls}`).style.gridRow).toBe('1');
    expect(bar.background || bar.backgroundColor).toMatch(/#d64545|rgb\(214, 69, 69\)/);
    expect(bar.opacity).toBe('1');
    expect(brand.style.getPropertyValue('--bar')).not.toBe('');
  });

  it('keeps the category heading ungreyed even when all its products are sold out', () => {
    store.set(state({ pils: 'SOLD_OUT', brand: 'SOLD_OUT', weizen: 'SOLD_OUT' }));
    expect(headings()).toEqual(['Beer', 'Wine']);
    const h = root.querySelector<HTMLElement>('.os-pl-h')!;
    expect(h.classList.contains('os-soldout')).toBe(false);
    expect(h.closest('.os-row')).toBeNull();
    expect(getComputedStyle(h).opacity).not.toBe('0.42');
    expect(rowIds()).toEqual(['pils', 'brand', 'weizen', 'riesling']); // still listed
  });
});

describe('UNAVAILABLE', () => {
  it('removes the row live without leaving an empty line', () => {
    const gap = row('pils').style.marginBottom;
    store.set(state({ brand: 'UNAVAILABLE' })); // synchronous: well within 1 s
    expect(rowIds()).toEqual(['pils', 'weizen', 'riesling']);
    const beer = root.querySelector('.os-pl-cat')!;
    // Heading followed directly by the remaining rows, same spacing as before.
    expect([...beer.children].map((c) => c.className)).toEqual(['os-pl-h', 'os-row', 'os-row']);
    expect(row('weizen').style.marginBottom).toBe(gap);
    expect(row('pils').nextElementSibling).toBe(row('weizen'));
  });

  it('drops an emptied category with its heading and restores it in place', () => {
    store.set(state({ riesling: 'UNAVAILABLE' }));
    expect(headings()).toEqual(['Beer']);
    expect(root.querySelectorAll('.os-pl-cat')).toHaveLength(1);
    store.set(state({ pils: 'UNAVAILABLE' }));
    store.set(state());
    expect(headings()).toEqual(['Beer', 'Wine']);
    expect(rowIds()).toEqual(['pils', 'brand', 'weizen', 'riesling']);
  });
});
