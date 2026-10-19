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

describe('heading modes', () => {
  // Beer with the subcategory "On tap"
  const withSub = (): LiveState => {
    const s = state();
    s.categories = [...s.categories, { id: 'tap', name: 'On tap', sort: 1, parentId: 'beer' }];
    s.products = [...s.products, product('lager', 'tap', 1)];
    return s;
  };
  const render = (patch: Partial<ProductListElement>): HTMLElement => {
    store.set(withSub());
    const el = renderElement({ ...list, ...patch }, testEnv(store)).el;
    document.body.appendChild(el);
    return el;
  };

  it('shows headings, keeps only their space, or leaves them out', () => {
    const shown = render({});
    expect([...shown.querySelectorAll<HTMLElement>('.os-pl-sh')].map((h) => [h.textContent, h.style.visibility])).toEqual([['On tap', '']]);

    // space only: the heading is there (same height as shown) but invisible
    const space = render({ categoryHeadings: false, categorySpace: true, subcategoryHeadings: false, subcategorySpace: true });
    const sh = space.querySelector<HTMLElement>('.os-pl-sh')!;
    expect(sh.style.visibility).toBe('hidden');
    expect(sh.getAttribute('aria-hidden')).toBe('true');
    expect(sh.style.margin).toBe(shown.querySelector<HTMLElement>('.os-pl-sh')!.style.margin);
    expect([...space.querySelectorAll<HTMLElement>('.os-pl-h')].map((h) => h.style.visibility)).toEqual(['hidden', 'hidden']);

    const none = render({ categoryHeadings: false, subcategoryHeadings: false });
    expect(none.querySelectorAll('.os-pl-h, .os-pl-sh').length).toBe(0);
    expect(none.querySelectorAll('.os-row').length).toBe(shown.querySelectorAll('.os-row').length);
  });
});

describe('variants', () => {
  const juices = (statuses: Record<string, ProductStatus>, patch: Partial<LiveProduct> = {}): LiveState => {
    const s = state();
    const v = (id: string, name: string) => ({ id, name, status: statuses[id] ?? ('AVAILABLE' as ProductStatus) });
    s.products = [...s.products, product('juice', 'wine', 2, 'AVAILABLE', { description: 'Fresh', variants: [v('o', 'Orange'), v('a', 'Apple'), v('g', 'Guava')], ...patch })];
    return s;
  };
  const show = (el: Partial<ProductListElement> = {}): HTMLElement => {
    const e = renderElement({ ...list, fields: { ...list.fields, description: true }, ...el }, testEnv(store)).el;
    document.body.appendChild(e);
    return e;
  };
  const line = (el: HTMLElement): HTMLElement | null => el.querySelector<HTMLElement>('.os-row[data-id="juice"] .os-vars');

  it('shows the variants like the description, separated by commas, below the description', () => {
    store.set(juices({}));
    const el = show();
    const vars = line(el)!;
    expect(vars.textContent).toBe('Orange, Apple, Guava');
    const desc = el.querySelector<HTMLElement>('.os-row[data-id="juice"] .os-desc')!;
    expect(vars.style.fontSize).toBe(desc.style.fontSize); // description style
    expect(desc.nextElementSibling).toBe(vars);
    expect(rule('.os-row .os-vars').style.gridRow).toBe('2');
    expect(rule('.os-row .os-desc~.os-vars').style.gridRow).toBe('3');
    expect(rule('.os-var').style.whiteSpace).toBe('nowrap'); // breaks only between variants
  });

  it('strikes through a sold-out variant only and leaves out variants that are not carried', () => {
    store.set(juices({ a: 'SOLD_OUT', g: 'UNAVAILABLE' }));
    const el = show();
    const vars = line(el)!;
    expect(vars.textContent).toBe('Orange, Apple');
    const items = [...vars.querySelectorAll<HTMLElement>('.os-var')];
    expect(items.map((i) => i.classList.contains('os-var-soldout'))).toEqual([false, true]);
    expect(el.querySelector('.os-row[data-id="juice"]')!.classList.contains('os-soldout')).toBe(false);
    const bar = rule('.os-var-soldout::after').style;
    expect(bar.background || bar.backgroundColor).toMatch(/#d64545|rgb\(214, 69, 69\)/);
    expect(getComputedStyle(items[1]!.firstElementChild!).opacity).toBe('0.42');
    expect(getComputedStyle(items[0]!.firstElementChild!).opacity).not.toBe('0.42');
  });

  it('updates live and shows the product sold out when every variant it offers is', () => {
    store.set(juices({}));
    const el = show();
    store.set(juices({ o: 'SOLD_OUT', a: 'SOLD_OUT', g: 'UNAVAILABLE' }));
    const juice = el.querySelector<HTMLElement>('.os-row[data-id="juice"]')!;
    expect(juice.classList.contains('os-soldout')).toBe(true);
    expect(line(el)!.querySelectorAll('.os-var-soldout')).toHaveLength(2);
    store.set(juices({ o: 'SOLD_OUT' }));
    expect(el.querySelector('.os-row[data-id="juice"]')!.classList.contains('os-soldout')).toBe(false);
    expect(line(el)!.querySelectorAll('.os-var-soldout')).toHaveLength(1);
  });

  it('can be switched off per element and needs no description', () => {
    store.set(juices({}, { description: '' }));
    expect(line(show({ fields: { ...list.fields, variants: false } }))).toBeNull();
    const el = show();
    expect(el.querySelector('.os-row[data-id="juice"] .os-desc')).toBeNull();
    expect(line(el)!.textContent).toBe('Orange, Apple, Guava');
  });
});

describe('variants, measured font', () => {
  it('strikes a sold-out variant through the middle of the small letters', () => {
    // Montserrat: ascent 0.968, x-height 0.517 of the font size.
    const fonts = { check: () => true, load: () => Promise.resolve([]) };
    Object.defineProperty(document, 'fonts', { value: fonts, configurable: true });
    const origGetContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = (() => ({ font: '', measureText: () => ({ fontBoundingBoxAscent: 96.8, actualBoundingBoxAscent: 51.7 }) })) as never;
    try {
      resetFontMetrics();
      const s = state();
      s.products = [...s.products, product('juice', 'wine', 2, 'AVAILABLE', { variants: [{ id: 'o', name: 'Orange', status: 'SOLD_OUT' }, { id: 'a', name: 'Apple', status: 'AVAILABLE' }] })];
      store.set(s);
      const el = renderElement(list, testEnv(store)).el;
      document.body.appendChild(el);
      const vars = el.querySelector<HTMLElement>('.os-row[data-id="juice"] .os-vars')!;
      expect(vars.style.getPropertyValue('--vtop')).toBe('0.7095em'); // 0.968 - 0.517 / 2
      // Until the font is measured: a little below the middle of the text box.
      expect(rule('.os-var-soldout::after').style.top).toMatch(/var\(--vtop, ?calc\(50% \+ 0?\.1em\)\)/);
    } finally {
      HTMLCanvasElement.prototype.getContext = origGetContext;
      Reflect.deleteProperty(document, 'fonts');
      resetFontMetrics();
    }
  });
});
