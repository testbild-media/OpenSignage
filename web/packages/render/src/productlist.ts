import type {
  LiveCategory,
  LiveProduct,
  ProductElement,
  ProductFields,
  ProductListElement,
  ProductOverrides,
  TextStyle,
  Typography,
} from '@opensignage/contracts';
import type { ElementHandle, RenderEnv } from './env';
import { formatMoney, formatQuantity } from './live';
import { applyTextStyle, fpx, mergeStyle, px } from './units';
import { whenAscender } from './fontmetrics';
import { wordmarkBox, wordmarkOf } from './wordmark';

interface Styles {
  heading: TextStyle;
  subheading: TextStyle;
  item: TextStyle;
  description: TextStyle;
  price: TextStyle;
}

function styles(t: Typography, o: ProductOverrides | undefined): Styles {
  return {
    heading: mergeStyle(t.heading, o?.heading),
    subheading: mergeStyle(t.subheading, o?.subheading),
    item: mergeStyle(t.item, o?.item),
    description: mergeStyle(t.description, o?.description),
    price: mergeStyle(t.price, o?.price),
  };
}

interface Group {
  cat: LiveCategory;
  products: LiveProduct[];
  subs: { cat: LiveCategory; products: LiveProduct[] }[];
}

/** Selects visible products (UNAVAILABLE never shown) grouped by category/subcategory. */
export function selectProducts(
  el: Pick<ProductListElement, 'source'>,
  categories: LiveCategory[],
  products: LiveProduct[],
): Group[] {
  const byId = new Map(categories.map((c) => [c.id, c]));
  const topOf = (catId: string): LiveCategory | undefined => {
    const c = byId.get(catId);
    if (!c) return undefined;
    return c.parentId ? byId.get(c.parentId) ?? c : c;
  };
  let chosen = products.filter((p) => p.status !== 'UNAVAILABLE');
  if (el.source.kind === 'categories') {
    const ids = new Set(el.source.ids);
    chosen = chosen.filter((p) => {
      const c = byId.get(p.categoryId);
      return ids.has(p.categoryId) || (c?.parentId !== undefined && ids.has(c.parentId));
    });
  } else if (el.source.kind === 'products') {
    const order = new Map(el.source.ids.map((id, i) => [id, i]));
    chosen = chosen.filter((p) => order.has(p.id));
    chosen.sort((a, b) => order.get(a.id)! - order.get(b.id)!);
  }
  const groups = new Map<string, Group>();
  const topSorted = [...categories].filter((c) => !c.parentId).sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name));
  for (const c of topSorted) groups.set(c.id, { cat: c, products: [], subs: [] });
  const sorted = el.source.kind === 'products' ? chosen : [...chosen].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name));
  for (const p of sorted) {
    const top = topOf(p.categoryId);
    if (!top) continue;
    const g = groups.get(top.id);
    if (!g) continue;
    const own = byId.get(p.categoryId);
    if (own && own.parentId) {
      let sub = g.subs.find((s) => s.cat.id === own.id);
      if (!sub) {
        sub = { cat: own, products: [] };
        g.subs.push(sub);
      }
      sub.products.push(p);
    } else {
      g.products.push(p);
    }
  }
  const out: Group[] = [];
  for (const g of groups.values()) {
    g.subs.sort((a, b) => a.cat.sort - b.cat.sort || a.cat.name.localeCompare(b.cat.name));
    if (g.products.length || g.subs.length) out.push(g);
  }
  return out;
}

function text(tag: string, cls: string, s: TextStyle, value: string): HTMLElement {
  const el = document.createElement(tag);
  el.className = cls;
  applyTextStyle(el, s);
  el.textContent = value;
  return el;
}

function cell(className: string): HTMLElement {
  const el = document.createElement('div');
  el.className = className;
  return el;
}

function rowSignature(p: LiveProduct, price: string, fields: ProductFields): string {
  return [p.id, p.name, p.status, price, fields.description ? p.description : '', p.imageAssetId ?? '', p.showImageInsteadOfName, p.imageScale ?? 100, p.quantity?.value, p.quantity?.unit].join('|');
}

function buildRow(p: LiveProduct, st: Styles, fields: ProductFields, priceAlign: string, env: RenderEnv): HTMLElement {
  const row = document.createElement('div');
  row.className = 'os-row' + (p.status === 'SOLD_OUT' ? ' os-soldout' : '');
  row.dataset.id = p.id;
  row.dataset.price = priceAlign;
  row.style.setProperty('--bar', fpx(Math.max(3, st.item.size * 0.14)));
  row.style.columnGap = fpx(st.item.size * 0.6);
  const price = formatMoney(env.live.price(p), env.live.language);
  // Empty cells keep their class: the grid places name, quantity and price explicitly in the
  // first row, so the sold-out bar strikes through that line only (not the description).
  const priceEl = fields.price ? text('div', 'os-price', st.price, price) : cell('os-price');
  const qty = fields.quantity ? text('div', 'os-qty', st.description, formatQuantity(p.quantity, env.live.language)) : cell('os-qty');
  let name: HTMLElement;
  const imgUrl = p.showImageInsteadOfName && p.imageAssetId ? env.assets.url(p.imageAssetId, 'display') : null;
  if (imgUrl) {
    // The image stands in for the name like a word in the item font: its letters are as
    // high as the tall letters of that font (l, b, d, capitals) and stand on the text
    // baseline; the width follows the aspect ratio (wordmark.ts). Rows with images keep the
    // height and baseline of rows with text. Until the font is measured: the capital height
    // (CSS cap); until the image is loaded and read: the whole image as letter height. The
    // product's logo size (imageScale, brand guidelines) scales the result; the logo stays on
    // the baseline.
    name = document.createElement('div');
    name.className = 'os-name';
    applyTextStyle(name, st.item);
    const img = document.createElement('img');
    img.className = 'os-logo';
    img.alt = p.name;
    const scale = (p.imageScale ?? 100) / 100;
    if (scale !== 1) img.style.setProperty('--logo-scale', String(scale));
    const fit = (): void =>
      whenAscender(st.item.font, st.item.bold ? '700' : '400', (r) => {
        const mark = img.complete && img.naturalWidth > 0 ? wordmarkOf(img) : null;
        const box = mark ? wordmarkBox(mark, r) : { height: r, offset: 0 };
        const round = (v: number): number => Math.round(v * scale * 10000) / 10000;
        img.style.height = `${round(box.height)}em`;
        img.style.verticalAlign = box.offset ? `${round(box.offset)}em` : '';
      });
    img.addEventListener('load', fit);
    img.src = imgUrl;
    fit();
    name.appendChild(img);
  } else {
    name = fields.name ? text('div', 'os-name', st.item, p.name) : cell('os-name');
  }
  if (priceAlign === 'left') row.append(priceEl, name, qty);
  else row.append(name, qty, priceEl);
  if (fields.description && p.description) row.append(text('div', 'os-desc', st.description, p.description));
  row.dataset.sig = rowSignature(p, price, fields);
  return row;
}

type ListLike = Pick<ProductListElement, 'source' | 'fields' | 'scale' | 'priceAlign' | 'overrides'> &
  Partial<Pick<ProductListElement, 'columns' | 'categoryHeadings' | 'subcategoryHeadings' | 'categorySpace' | 'subcategorySpace' | 'rowGap' | 'columnGap'>>;

/** A heading that is not shown but keeps its space (the gap between the groups stays). */
function hide(h: HTMLElement): void {
  h.style.visibility = 'hidden';
  h.setAttribute('aria-hidden', 'true');
}

/** Renders a product list (or a single product) that updates live, row by row. */
export function renderProductList(el: ListLike, env: RenderEnv): ElementHandle {
  const root = document.createElement('div');
  root.className = 'os-pl';
  const factor = el.scale || 1;
  const t = env.typography;
  const scaled = (s: TextStyle): TextStyle => ({ ...s, size: s.size * factor, letterSpacing: s.letterSpacing * factor });
  const base = styles(t, el.overrides);
  const st: Styles = {
    heading: scaled(base.heading),
    subheading: scaled(base.subheading),
    item: scaled(base.item),
    description: scaled(base.description),
    price: scaled(base.price),
  };
  root.style.columnCount = String(el.columns ?? 1);
  root.style.columnGap = px(el.columnGap ?? 40);
  root.style.columnFill = 'auto';
  let structure = '';
  const rows = new Map<string, HTMLElement>();

  const render = (): void => {
    const live = env.live.state;
    const groups = live ? selectProducts(el, live.categories, live.products) : [];
    const newStructure = JSON.stringify(groups.map((g) => [g.cat.id, g.cat.name, g.products.map((p) => p.id), g.subs.map((s) => [s.cat.id, s.cat.name, s.products.map((p) => p.id)])]));
    if (newStructure === structure) {
      // Same rows in the same order: patch only rows whose content changed.
      for (const g of groups) {
        for (const p of [...g.products, ...g.subs.flatMap((s) => s.products)]) {
          const old = rows.get(p.id);
          const price = formatMoney(env.live.price(p), env.live.language);
          if (old && old.dataset.sig !== rowSignature(p, price, el.fields)) {
            const fresh = buildRow(p, st, el.fields, el.priceAlign, env);
            fresh.style.marginBottom = old.style.marginBottom;
            old.replaceWith(fresh);
            rows.set(p.id, fresh);
          }
        }
      }
      return;
    }
    structure = newStructure;
    rows.clear();
    root.replaceChildren();
    const gap = px(el.rowGap ?? 6);
    for (const g of groups) {
      const cat = document.createElement('div');
      cat.className = 'os-pl-cat';
      if (el.categoryHeadings || el.categorySpace) {
        const h = text('div', 'os-pl-h', st.heading, g.cat.name);
        h.style.marginBottom = gap;
        if (!el.categoryHeadings) hide(h);
        cat.appendChild(h);
      }
      const addRows = (list: LiveProduct[], into: HTMLElement): void => {
        for (const p of list) {
          const r = buildRow(p, st, el.fields, el.priceAlign, env);
          r.style.marginBottom = gap;
          rows.set(p.id, r);
          into.appendChild(r);
        }
      };
      addRows(g.products, cat);
      for (const s of g.subs) {
        if (el.subcategoryHeadings || el.subcategorySpace) {
          const sh = text('div', 'os-pl-sh', st.subheading, s.cat.name);
          sh.style.margin = `${gap} 0`;
          if (!el.subcategoryHeadings) hide(sh);
          cat.appendChild(sh);
        }
        addRows(s.products, cat);
      }
      cat.style.marginBottom = px((el.rowGap ?? 6) * 3);
      root.appendChild(cat);
    }
  };
  render();
  const unsub = env.live.subscribe(render);
  return {
    el: root,
    start() {},
    stop() {},
    destroy() {
      unsub();
    },
  };
}

/** A single product rendered like a list row. */
export function renderProduct(el: ProductElement, env: RenderEnv): ElementHandle {
  return renderProductList(
    { source: { kind: 'products', ids: [el.productId] }, fields: el.fields, scale: el.scale, priceAlign: el.priceAlign, overrides: el.overrides, columns: 1 },
    env,
  );
}
