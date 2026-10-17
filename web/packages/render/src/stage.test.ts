// Stage behaviour on the player: scale modes, sharp rendering of other design sizes and
// per-slide transitions with continuing backgrounds and pinned elements.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  Background,
  ImageElement,
  LiveState,
  ProductListElement,
  ScaleMode,
  ShapeElement,
  Slide,
  TextElement,
  Transition,
} from '@opensignage/contracts';
import { Stage, type StageSlide } from './stage';
import { RENDER_CSS } from './css';
import { LiveStore } from './live';
import { renderElement } from './elements';
import type { RenderEnv } from './env';
import {
  TEXT_STYLE,
  base,
  fakeAnimations,
  fakeMedia,
  fakeResizeObserver,
  setBox,
  slide,
  testEnv,
  type FakeAnimations,
  type FakeMedia,
  type FakeResizeObservers,
} from './testing';

let anims: FakeAnimations;
let ro: FakeResizeObservers;
let media: FakeMedia;
let host: HTMLElement;
let counter = 0;

beforeEach(() => {
  vi.useFakeTimers();
  anims = fakeAnimations();
  ro = fakeResizeObserver();
  media = fakeMedia({ durationFor: () => 60 });
  host = document.createElement('div');
  document.body.replaceChildren(host);
});

afterEach(() => {
  media.restore();
  anims.restore();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  document.body.replaceChildren();
});

/** A stage on a screen of the given CSS size (the root box is what the browser lays out). */
function newStage(screenW: number, screenH: number, mode: ScaleMode = 'contain'): Stage {
  const st = new Stage(host, mode);
  setBox(st.root, screenW, screenH);
  ro.resize();
  return st;
}

function ss(s: Slide, width = 1920, height = 1080, env: RenderEnv = testEnv()): StageSlide {
  return { slide: s, width, height, env, key: `${s.id}#${++counter}` };
}

/** Shows a slide and lets its transition run to the end. */
async function showDone(st: Stage, next: StageSlide): Promise<void> {
  const p = st.show(next);
  await vi.advanceTimersByTimeAsync(Math.max(0, next.slide.transition.ms) + 1);
  await p;
}

function canvases(st: Stage): HTMLElement[] {
  return [...st.root.children].filter((c): c is HTMLElement => c.classList.contains('os-canvas'));
}

function canvas(st: Stage): HTMLElement {
  return canvases(st).at(-1)!;
}

/** Scale factor, laid-out canvas size and its offset from the screen's top left corner. */
function geometry(st: Stage): { s: number; w: number; h: number; x: number; y: number } {
  const c = canvas(st);
  const r = st.root.getBoundingClientRect();
  // .os-canvas is positioned at left/top 50% and pulled back by its negative margins.
  return {
    s: parseFloat(c.style.getPropertyValue('--s')),
    w: parseFloat(c.style.width),
    h: parseFloat(c.style.height),
    x: r.width / 2 + parseFloat(c.style.marginLeft),
    y: r.height / 2 + parseFloat(c.style.marginTop),
  };
}

function text(id: string, value: string, patch: Partial<TextElement> = {}): TextElement {
  return {
    ...base(id, { x: 100, y: 100, w: 600, h: 120 }),
    type: 'text',
    doc: { paragraphs: [{ runs: [{ text: value }] }] },
    style: TEXT_STYLE,
    valign: 'top',
    ...patch,
  };
}

function logo(id: string, patch: Partial<ImageElement> = {}): ImageElement {
  return {
    ...base(id, { x: 1700, y: 40, w: 160, h: 80, pinned: true }),
    type: 'image',
    assetId: 'logo',
    fit: 'contain',
    ...patch,
  };
}

const VIDEO_BG: Background = { type: 'video', color: '#000000', assetId: 'loop', fit: 'cover' };

// ---------------------------------------------------------------- scale modes

describe('scale modes per device (fit / fill / 1:1)', () => {
  // [screen, design, expected factor per mode]; 1:1 = design pixel = screen pixel (DPR 1).
  const table: [number, number, number, number, Record<ScaleMode, number>][] = [
    [1920, 1080, 1920, 1080, { contain: 1, cover: 1, none: 1 }],
    [1920, 1080, 1280, 720, { contain: 1.5, cover: 1.5, none: 1 }],
    [1280, 800, 1920, 1080, { contain: 1280 / 1920, cover: 800 / 1080, none: 1 }],
    [1280, 800, 1280, 720, { contain: 1, cover: 800 / 720, none: 1 }],
    [3840, 2160, 1920, 1080, { contain: 2, cover: 2, none: 1 }],
    [3840, 2160, 1280, 720, { contain: 3, cover: 3, none: 1 }],
  ];
  const cases = table.flatMap(([sw, sh, dw, dh, want]) =>
    (['contain', 'cover', 'none'] as const).map((mode) => ({ sw, sh, dw, dh, mode, s: want[mode] })),
  );

  it.each(cases)('$sw×$sh screen, $dw×$dh design, $mode → factor $s', async ({ sw, sh, dw, dh, mode, s }) => {
    const st = newStage(sw, sh, mode);
    await st.show(ss(slide('a', { elements: [text('t', 'Hello')] }), dw, dh));
    const g = geometry(st);
    expect(g.s).toBeCloseTo(s, 4);
    expect(g.w).toBeCloseTo(dw * s, 2);
    expect(g.h).toBeCloseTo(dh * s, 2);
    // Always centred: equal margins (letterbox / pillarbox) or equal overhang on both sides.
    expect(g.x).toBeCloseTo((sw - dw * s) / 2, 2);
    expect(g.y).toBeCloseTo((sh - dh * s) / 2, 2);
    if (mode === 'contain') {
      // Whole slide visible, one side touches the screen edges.
      expect(g.w).toBeLessThanOrEqual(sw + 0.01);
      expect(g.h).toBeLessThanOrEqual(sh + 0.01);
      expect(Math.min(sw - g.w, sh - g.h)).toBeCloseTo(0, 2);
    } else if (mode === 'cover') {
      // Screen completely filled, the overhang is cut off by the stage.
      expect(g.w).toBeGreaterThanOrEqual(sw - 0.01);
      expect(g.h).toBeGreaterThanOrEqual(sh - 0.01);
      expect(Math.min(g.w - sw, g.h - sh)).toBeCloseTo(0, 2);
    } else {
      expect(g.w).toBe(dw);
      expect(g.h).toBe(dh);
    }
    st.destroy();
  });

  it('1:1 maps design pixels to device pixels on high-DPI screens', async () => {
    vi.stubGlobal('devicePixelRatio', 2);
    expect(window.devicePixelRatio).toBe(2);
    const st = newStage(1920, 1080, 'none'); // 3840×2160 panel at 200 %
    await st.show(ss(slide('a'), 1920, 1080));
    const g = geometry(st);
    expect(g.s).toBe(0.5);
    expect(g.w * 2).toBe(1920);
    expect(g.x).toBe(480);
  });

  it('fills letterbox margins with the slide background colour', async () => {
    const st = newStage(1280, 800, 'contain');
    await st.show(ss(slide('a', { background: { type: 'color', color: '#123456' } })));
    expect(st.root.style.backgroundColor).toBe('rgb(18, 52, 86)');
    expect(geometry(st).y).toBeCloseTo(40, 2);
  });

  it('a mode change takes effect immediately, without reload or re-render', async () => {
    const st = newStage(1280, 800, 'contain');
    await st.show(ss(slide('a', { elements: [text('t', 'Hello')] })));
    const c = canvas(st);
    const el = c.querySelector('[data-element-id="t"]');
    const animations = anims.calls.length;
    expect(geometry(st).s).toBeCloseTo(1280 / 1920, 4);

    st.setScaleMode('cover');
    expect(geometry(st).s).toBeCloseTo(800 / 1080, 4); // synchronously, no new show()
    expect(geometry(st).x).toBeCloseTo((1280 - 1920 * (800 / 1080)) / 2, 2);
    st.setScaleMode('none');
    expect(geometry(st)).toMatchObject({ s: 1, w: 1920, h: 1080, x: -320, y: -140 });
    st.setScaleMode('contain');
    expect(geometry(st)).toMatchObject({ w: 1280, h: 720, x: 0, y: 40 });

    // Same canvas, same element nodes, nothing animated: the slide was not rebuilt.
    expect(canvas(st)).toBe(c);
    expect(c.querySelector('[data-element-id="t"]')).toBe(el);
    expect(anims.calls.length).toBe(animations);
  });

  it('follows a change of the screen size (display mode switched remotely)', async () => {
    const st = newStage(1280, 800, 'contain');
    await st.show(ss(slide('a')));
    setBox(st.root, 3840, 2160);
    ro.resize();
    expect(geometry(st)).toMatchObject({ s: 2, w: 3840, h: 2160, x: 0, y: 0 });
  });
});

// ---------------------------------------------------------------- design sizes

describe('1280×720 presentation on a 1920×1080 screen renders sharp', () => {
  const CALC = /^calc\(var\(--s\) \* -?\d+(\.\d+)?px\)$|^0px$/;

  function liveWithLogoProduct(): LiveStore {
    const store = new LiveStore();
    const state: LiveState = {
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
      categories: [{ id: 'beer', name: 'Beer', sort: 1 }],
      products: [
        {
          id: 'p1',
          name: 'Pils',
          description: 'crisp',
          categoryId: 'beer',
          type: 'drink',
          price: { amount: 450, currency: 'EUR' },
          status: 'AVAILABLE',
          showImageInsteadOfName: false,
          sort: 1,
        },
        {
          id: 'p2',
          name: 'Brand',
          description: '',
          categoryId: 'beer',
          type: 'drink',
          price: { amount: 500, currency: 'EUR' },
          status: 'SOLD_OUT',
          imageAssetId: 'brand',
          showImageInsteadOfName: true,
          sort: 2,
        },
      ],
      serverTime: new Date().toISOString(),
    };
    store.set(state);
    return store;
  }

  const elements: Slide['elements'] = [
    text('t', '', {
      x: 40,
      y: 30,
      w: 600,
      h: 120,
      padding: [8, 12, 8, 12],
      doc: {
        paragraphs: [{ runs: [{ text: 'Happy ' }, { text: 'Hour', marks: { size: 48, bold: true } }] }],
      },
    }),
    {
      ...base('img', { x: 640, y: 0, w: 640, h: 360 }),
      type: 'image',
      assetId: 'photo',
      fit: 'cover',
    } satisfies ImageElement,
    {
      ...base('rect', { x: 40, y: 400, w: 300, h: 200 }),
      type: 'shape',
      shape: 'rect',
      fill: '#ff0000',
      stroke: { color: '#ffffff', width: 3, dash: 'solid' },
      radius: 12,
    } satisfies ShapeElement,
    {
      ...base('line', { x: 400, y: 400, w: 400, h: 20 }),
      type: 'shape',
      shape: 'line',
      stroke: { color: '#ffffff', width: 4, dash: 'solid' },
    } satisfies ShapeElement,
    {
      ...base('list', { x: 700, y: 380, w: 540, h: 300 }),
      type: 'productList',
      source: { kind: 'all', ids: [] },
      columns: 1,
      fields: { name: true, quantity: false, description: true, price: true },
      categoryHeadings: true,
      scale: 1,
      priceAlign: 'right',
    } satisfies ProductListElement,
  ];

  async function shown(): Promise<{ st: Stage; c: HTMLElement; purposes: [string, string][] }> {
    const purposes: [string, string][] = [];
    const env: RenderEnv = {
      ...testEnv(liveWithLogoProduct()),
      assets: {
        url(id, purpose) {
          purposes.push([id, purpose]);
          return `/media/${id}/${purpose}`;
        },
      },
    };
    const st = newStage(1920, 1080, 'contain');
    await st.show(
      ss(
        slide('a', {
          elements,
          background: { type: 'image', color: '#000000', assetId: 'wall', fit: 'cover' },
        }),
        1280,
        720,
        env,
      ),
    );
    return { st, c: canvas(st), purposes };
  }

  it('lays the canvas out at the output size instead of magnifying a 1280×720 layer', async () => {
    const { st, c } = await shown();
    expect(geometry(st)).toMatchObject({ s: 1.5, w: 1920, h: 1080, x: 0, y: 0 });
    // No transform anywhere on the way from the screen to the elements (a scale() would
    // rasterise at design size and blur when composited).
    for (const el of [st.root, c, ...c.querySelectorAll<HTMLElement>('*')]) {
      expect(el.style?.transform ?? '').not.toMatch(/scale|matrix/);
    }
    expect(c.style.transform).toBe('');
    expect(RENDER_CSS).not.toMatch(/scale\(|zoom\s*:/);
  });

  it('sizes every element box, font, padding and stroke through var(--s)', async () => {
    const { c } = await shown();
    const boxes = [...c.querySelectorAll<HTMLElement>('.os-el')];
    expect(boxes).toHaveLength(elements.length);
    for (const b of boxes) {
      for (const v of [b.style.left, b.style.top, b.style.width, b.style.height]) expect(v).toMatch(CALC);
    }
    const t = c.querySelector<HTMLElement>('[data-element-id="t"]')!;
    expect(t.style.left).toBe('calc(var(--s) * 40px)');
    expect(t.style.width).toBe('calc(var(--s) * 600px)');
    expect(t.style.fontSize).toBe('calc(var(--s) * var(--fit, 1) * 24px)');
    expect(t.style.padding).toBe(
      'calc(var(--s) * 8px) calc(var(--s) * 12px) calc(var(--s) * 8px) calc(var(--s) * 12px)',
    );
    const run = [...t.querySelectorAll<HTMLElement>('span')].find((s) => s.textContent === 'Hour')!;
    expect(run.style.fontSize).toBe('calc(var(--s) * var(--fit, 1) * 48px)');

    const rect = c.querySelector<HTMLElement>('[data-element-id="rect"]')!;
    expect(rect.style.border).toContain('calc(var(--s) * 3px)');
    expect(rect.style.borderRadius).toBe('calc(var(--s) * 12px)');

    // Lines are vector graphics in design units, stretched by their (scaled) box.
    const svg = c.querySelector('[data-element-id="line"] svg')!;
    expect(svg.getAttribute('viewBox')).toBe('0 0 400 20');
    expect(svg.querySelector('line')!.getAttribute('stroke-width')).toBe('4');

    const list = c.querySelector<HTMLElement>('[data-element-id="list"]')!;
    expect(list.querySelector<HTMLElement>('.os-pl-h')!.style.fontSize).toBe(
      'calc(var(--s) * var(--fit, 1) * 24px)',
    );
    const row = list.querySelector<HTMLElement>('[data-id="p1"]')!;
    expect(row.querySelector<HTMLElement>('.os-name')!.style.fontSize).toBe(
      'calc(var(--s) * var(--fit, 1) * 24px)',
    );
    expect(row.querySelector<HTMLElement>('.os-price')!.style.fontSize).toBe(
      'calc(var(--s) * var(--fit, 1) * 24px)',
    );
    expect(row.style.getPropertyValue('--bar')).toMatch(
      /^calc\(var\(--s\) \* var\(--fit, 1\) \* [\d.]+px\)$/,
    );
    // A product image instead of the name is as high as the capital letters (CSS cap) of
    // its cell's font – and that font size scales through var(--s) like every text.
    const imageCell = list.querySelector<HTMLElement>('[data-id="p2"] .os-name')!;
    expect(imageCell.style.fontSize).toBe('calc(var(--s) * var(--fit, 1) * 24px)');
    expect(imageCell.querySelector<HTMLElement>('img.os-logo')!.style.height).toBe('');
  });

  it('images fill their scaled box and request the display variant chosen for the screen', async () => {
    const { c, purposes } = await shown();
    const img = c.querySelector<HTMLImageElement>('[data-element-id="img"] img')!;
    // No intrinsic pixel size: the image is drawn at the box size (CSS width/height 100 %).
    expect(img.getAttribute('width')).toBeNull();
    expect(img.style.width).toBe('');
    expect(RENDER_CSS).toContain('.os-el img{display:block;width:100%;height:100%}');
    // "display" = the variant the agent caches for this screen width (i1920 up to 1920 px,
    // the original above; agent/syncer Select). Never the thumbnail on the player.
    expect(img.getAttribute('src')).toBe('/media/photo/display');
    expect(purposes).toEqual(
      expect.arrayContaining([
        ['wall', 'display'],
        ['photo', 'display'],
        ['brand', 'display'],
      ]),
    );
    expect(purposes.filter(([, p]) => p !== 'display')).toEqual([]);
    // The editor's thumbnails use the small variant instead (it keeps transparency, unlike
    // the media library thumbnail on grey).
    const thumb = renderElement(elements[1]!, { ...testEnv(), mode: 'thumb' });
    expect(thumb.el.querySelector('img')!.getAttribute('src')).toBe('/media/photo/small');
  });
});

// ---------------------------------------------------------------- transitions

describe('transitions per slide', () => {
  function contentLayers(st: Stage): HTMLElement[] {
    return [...canvas(st).querySelectorAll<HTMLElement>('.os-contentl > .os-content')];
  }

  const cases: { t: Transition; incoming: Keyframe[]; outgoing: Keyframe[] }[] = [
    {
      t: { type: 'fade', ms: 700 },
      incoming: [{ opacity: 0 }, { opacity: 1 }],
      outgoing: [{ opacity: 1 }, { opacity: 0 }],
    },
    {
      t: { type: 'zoom', ms: 700 },
      incoming: [
        { opacity: 0, transform: 'scale(1.08)' },
        { opacity: 1, transform: 'scale(1)' },
      ],
      outgoing: [
        { opacity: 1, transform: 'scale(1)' },
        { opacity: 0, transform: 'scale(0.96)' },
      ],
    },
    {
      t: { type: 'slide', ms: 700, direction: 'left' },
      incoming: [{ transform: 'translateX(100%)' }, { transform: 'translateX(0)' }],
      outgoing: [{ transform: 'translateX(0)' }, { transform: 'translateX(-100%)' }],
    },
    {
      t: { type: 'slide', ms: 700, direction: 'right' },
      incoming: [{ transform: 'translateX(-100%)' }, { transform: 'translateX(0)' }],
      outgoing: [{ transform: 'translateX(0)' }, { transform: 'translateX(100%)' }],
    },
    {
      t: { type: 'slide', ms: 700, direction: 'up' },
      incoming: [{ transform: 'translateY(100%)' }, { transform: 'translateY(0)' }],
      outgoing: [{ transform: 'translateY(0)' }, { transform: 'translateY(-100%)' }],
    },
    {
      t: { type: 'slide', ms: 700, direction: 'down' },
      incoming: [{ transform: 'translateY(-100%)' }, { transform: 'translateY(0)' }],
      outgoing: [{ transform: 'translateY(0)' }, { transform: 'translateY(100%)' }],
    },
  ];

  it.each(cases)(
    '$t.type $t.direction runs for the slide duration with transform/opacity only',
    async ({ t, incoming, outgoing }) => {
      const st = newStage(1920, 1080);
      await st.show(ss(slide('a', { elements: [text('a1', 'A')] })));
      const [oldLayer] = contentLayers(st);
      let done = false;
      const p = st.show(ss(slide('b', { transition: t, elements: [text('b1', 'B')] }))).then(() => {
        done = true;
      });
      const layers = contentLayers(st);
      expect(layers).toHaveLength(2); // both visible while the transition runs
      const newLayer = layers[1]!;
      const inAnim = anims.on(newLayer);
      const outAnim = anims.on(oldLayer!);
      expect(inAnim.map((c) => c.keyframes)).toEqual([incoming]);
      expect(outAnim.map((c) => c.keyframes)).toEqual([outgoing]);
      for (const c of anims.calls) {
        expect(c.options.duration).toBe(700);
        for (const k of c.keyframes)
          expect(Object.keys(k).every((p) => p === 'opacity' || p === 'transform')).toBe(true);
      }
      await vi.advanceTimersByTimeAsync(699);
      expect(done).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      await p;
      expect(done).toBe(true);
      expect(contentLayers(st)).toEqual([newLayer]);
      expect(oldLayer!.isConnected).toBe(false);
    },
  );

  it('cut switches at once without animating anything', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { elements: [text('a1', 'A')] })));
    await st.show(ss(slide('b', { transition: { type: 'cut', ms: 900 }, elements: [text('b1', 'B')] })));
    expect(anims.calls).toEqual([]);
    expect(contentLayers(st)).toHaveLength(1);
    expect(canvas(st).querySelector('[data-element-id="b1"]')).not.toBeNull();
    expect(canvas(st).querySelector('[data-element-id="a1"]')).toBeNull();
  });

  it('keeps an identical background video playing on the same node (no restart, no transition)', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { background: VIDEO_BG, elements: [text('a1', 'A')] })));
    const v = st.backgroundVideo()!;
    const bgNode = v.parentElement!;
    await vi.advanceTimersByTimeAsync(5000);
    const pos = media.state(v).currentTime;
    expect(pos).toBeGreaterThanOrEqual(4.5);

    await showDone(
      st,
      ss(
        slide('b', {
          background: { ...VIDEO_BG },
          transition: { type: 'slide', ms: 800, direction: 'left' },
          elements: [text('b1', 'B')],
        }),
      ),
    );
    expect(st.backgroundVideo()).toBe(v);
    expect(v.isConnected).toBe(true);
    expect(v.parentElement).toBe(bgNode);
    const s = media.state(v);
    expect({ plays: s.plays, pauses: s.pauses, loads: s.loads }).toEqual({ plays: 1, pauses: 0, loads: 0 });
    expect(s.currentTime).toBeGreaterThan(pos); // continued, not reset to 0
    expect(anims.on(bgNode, true)).toEqual([]);
    expect(anims.calls.length).toBeGreaterThan(0); // only the content moved
    expect(st.root.querySelectorAll('video')).toHaveLength(1);
  });

  it('a different background transitions and releases the outgoing decoder', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { background: VIDEO_BG })));
    const oldVideo = st.backgroundVideo()!;
    const p = st.show(
      ss(
        slide('b', { background: { ...VIDEO_BG, assetId: 'other' }, transition: { type: 'fade', ms: 600 } }),
      ),
    );
    const newVideo = st.backgroundVideo()!;
    expect(newVideo).not.toBe(oldVideo);
    // At most one background decoder: the outgoing video is paused when the transition starts.
    expect(oldVideo.paused).toBe(true);
    expect(newVideo.paused).toBe(false);
    expect(anims.on(newVideo.parentElement!).map((c) => c.keyframes)).toEqual([
      [{ opacity: 0 }, { opacity: 1 }],
    ]);
    expect(anims.on(oldVideo.parentElement!).map((c) => c.keyframes)).toEqual([
      [{ opacity: 1 }, { opacity: 0 }],
    ]);
    await vi.advanceTimersByTimeAsync(600);
    await p;
    expect(oldVideo.isConnected).toBe(false);
    expect(oldVideo.hasAttribute('src')).toBe(false); // decoder memory freed
    expect(st.root.querySelectorAll('video')).toHaveLength(1);
  });

  it('a pinned identical logo stays the same node while the other elements change', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { elements: [logo('logo-a'), text('a1', 'Beer')] })));
    const logoEl = canvas(st).querySelector<HTMLElement>('[data-element-id="logo-a"]')!;
    const a1 = canvas(st).querySelector('[data-element-id="a1"]')!;
    expect(logoEl.parentElement!.classList.contains('os-pinnedl')).toBe(true);

    // Same logo on the next slide (other id/name, same content and placement).
    const p = st.show(
      ss(
        slide('b', {
          transition: { type: 'slide', ms: 800, direction: 'left' },
          elements: [logo('logo-b', { name: 'Logo' }), text('b1', 'Wine')],
        }),
      ),
    );
    expect(anims.calls.filter((c) => c.el.contains(logoEl))).toEqual([]); // never moved or faded
    await vi.advanceTimersByTimeAsync(800);
    await p;
    expect(canvas(st).querySelector('.os-pinnedl')!.children).toHaveLength(1);
    expect(canvas(st).querySelector('.os-pinnedl')!.firstElementChild).toBe(logoEl);
    expect(logoEl.isConnected).toBe(true);
    expect(a1.isConnected).toBe(false);
    expect(canvas(st).querySelector('[data-element-id="b1"]')).not.toBeNull();
    expect(anims.calls.some((c) => c.el.contains(canvas(st).querySelector('[data-element-id="b1"]')!))).toBe(
      true,
    );
  });

  it('a pinned element that differs or disappears fades in place (never slides)', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { elements: [logo('logo'), logo('badge', { x: 40, assetId: 'badge' })] })));
    const oldLogo = canvas(st).querySelector<HTMLElement>('[data-element-id="logo"]')!;
    const badge = canvas(st).querySelector<HTMLElement>('[data-element-id="badge"]')!;
    const p = st.show(
      ss(
        slide('b', {
          transition: { type: 'slide', ms: 500, direction: 'up' },
          elements: [logo('logo', { y: 900 })],
        }),
      ),
    );
    const newLogo = canvas(st).querySelector<HTMLElement>('.os-pinnedl > :last-child')!;
    expect(newLogo).not.toBe(oldLogo);
    expect(anims.on(newLogo).map((c) => c.keyframes)).toEqual([[{ opacity: 0 }, { opacity: 1 }]]);
    expect(anims.on(oldLogo).map((c) => c.keyframes)).toEqual([[{ opacity: 1 }, { opacity: 0 }]]);
    expect(anims.on(badge).map((c) => c.keyframes)).toEqual([[{ opacity: 1 }, { opacity: 0 }]]);
    await vi.advanceTimersByTimeAsync(500);
    await p;
    expect(oldLogo.isConnected).toBe(false);
    expect(badge.isConnected).toBe(false);
    expect(canvas(st).querySelector('.os-pinnedl')!.children).toHaveLength(1);
  });

  it('a presentation with another design size swaps the whole canvas with the transition', async () => {
    const st = newStage(1920, 1080);
    await st.show(ss(slide('a', { elements: [text('a1', 'A')] }), 1920, 1080));
    const oldCanvas = canvas(st);
    const p = st.show(
      ss(slide('b', { transition: { type: 'fade', ms: 400 }, elements: [text('b1', 'B')] }), 1280, 720),
    );
    await vi.advanceTimersByTimeAsync(400);
    await p;
    expect(anims.on(oldCanvas).map((c) => c.keyframes)).toEqual([[{ opacity: 1 }, { opacity: 0 }]]);
    expect(canvases(st)).toHaveLength(1);
    expect(oldCanvas.isConnected).toBe(false);
    expect(geometry(st)).toMatchObject({ s: 1.5, w: 1920, h: 1080 });
  });
});
