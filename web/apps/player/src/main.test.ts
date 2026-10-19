// The player app driven through its agent link: IP overlay at start, scale mode changes
// without reload, asset variants and the zone ticker overlay vs. ticker elements.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AgentToPlayer,
  ImageElement,
  LiveState,
  Manifest,
  Money,
  PlayerConfig,
  PlayerMode,
  Presentation,
  ScheduleTicker,
  Slide,
  TextElement,
  TickerElement,
} from '@opensignage/contracts';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import indexHtml from '../index.html?raw';
import {
  TEXT_STYLE,
  base,
  fakeAnimations,
  fakeLayout,
  fakeResizeObserver,
  presentation,
  setBox,
  slide,
  type FakeAnimations,
  type FakeResizeObservers,
} from '@opensignage/render/testing.ts';

class FakeWebSocket {
  static readonly OPEN = 1;
  static instances: FakeWebSocket[] = [];
  readyState = FakeWebSocket.OPEN;
  sent: unknown[] = [];
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(readonly url: string) {
    FakeWebSocket.instances.push(this);
  }
  send(data: string): void {
    this.sent.push(JSON.parse(data));
  }
  close(): void {}
}

const MODE: PlayerMode = {
  kind: 'playing',
  ips: ['192.168.1.20', '100.64.0.7'],
  webguiUrl: 'http://192.168.1.20:8080',
  language: 'en',
};
const CONFIG: PlayerConfig = {
  scaleMode: 'contain',
  perf: 'standard',
  plasmaShift: false,
  showIpOverlay: true,
  timezone: 'Europe/Berlin',
  clockOffsetMs: 0,
};

let anims: FakeAnimations;
let ro: FakeResizeObservers;
let restoreLayout: () => void;

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  anims = fakeAnimations();
  ro = fakeResizeObserver();
  restoreLayout = fakeLayout((el) => {
    if (el.tagName === 'SPAN' && el.parentElement?.classList.contains('os-ticker-track'))
      return { width: (el.textContent ?? '').length * 10, height: 30 };
    if (el.classList.contains('os-ticker')) return { width: 1920, height: 64 };
    return null;
  });
  FakeWebSocket.instances = [];
  vi.stubGlobal('WebSocket', FakeWebSocket);
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) })),
  );
  const doc = new DOMParser().parseFromString(indexHtml, 'text/html');
  doc.querySelectorAll('script').forEach((s) => s.remove());
  document.body.replaceChildren(...[...doc.body.childNodes].map((n) => document.importNode(n, true)));
  const style = document.createElement('style');
  // Vitest stubs CSS imports (even ?raw), so the stylesheet is read from disk.
  style.textContent = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'player.css'), 'utf8');
  document.head.replaceChildren(style);
  await import('./main');
  await vi.advanceTimersByTimeAsync(0); // boot(): font manifest, then connect()
});

afterEach(() => {
  restoreLayout();
  anims.restore();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function ws(): FakeWebSocket {
  expect(FakeWebSocket.instances).toHaveLength(1);
  return FakeWebSocket.instances[0]!;
}

async function push(...msgs: AgentToPlayer[]): Promise<void> {
  for (const m of msgs) {
    ws().onmessage!({ data: JSON.stringify(m) });
    await vi.advanceTimersByTimeAsync(0);
  }
}

const $ = (id: string): HTMLElement => document.getElementById(id)!;
const visible = (id: string): boolean => !$(id).classList.contains('hidden');

function manifest(
  presentations: Presentation[],
  playlistItems = presentations.map((p) => ({
    id: `i-${p.id}`,
    kind: 'presentation' as const,
    refId: p.id,
    play: { mode: 'repeat' as const, count: 1 },
  })),
): Manifest {
  return {
    id: 'd1',
    zoneId: 'z',
    hash: 'h1',
    builtAt: '2026-10-11T10:00:00Z',
    minAgentVersion: '0.0.0',
    timezone: 'Europe/Berlin',
    rules: [],
    playlists: [{ id: 'L', name: 'L', items: playlistItems }],
    presentations,
    products: { imageAssetIds: [] },
    assets: [],
    fonts: [],
  };
}

type LivePayload = { state: LiveState; prices: Record<string, Money>; ticker: ScheduleTicker | null };

/** A live message: zone state plus the ticker of the active time schedule rule (null: no rule
 * shows one). The legacy zone ticker in the state stays empty, as the server sends it. */
function liveState(text: string, showTicker = true): LivePayload {
  return {
    state: baseState(),
    prices: {},
    ticker: showTicker
      ? {
          text,
          ruleId: 'r-happy',
          template: { font: 'Montserrat', size: 40, color: '#ffffff', background: '#202020', speed: 120, direction: 'left', height: 64, position: 'bottom' },
        }
      : null,
  };
}

function baseState(): LiveState {
  return {
    zone: { id: 'z', name: 'Bar', state: 'OPEN' },
    ticker: {
      text: '',
      visible: false,
      template: {
        font: 'Montserrat',
        size: 40,
        color: '#ffffff',
        background: '#202020',
        speed: 120,
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
        description: '',
        categoryId: 'beer',
        type: 'drink',
        price: { amount: 450, currency: 'EUR' },
        status: 'AVAILABLE',
        showImageInsteadOfName: false,
        sort: 1,
      },
    ],
    serverTime: '2026-10-11T10:00:00Z',
  };
}

/** Starts playback of the given presentations (agent snapshot order). */
async function startPlaying(presentations: Presentation[], live: LivePayload = liveState('', false)): Promise<void> {
  await push(
    { t: 'mode', p: MODE },
    { t: 'config', p: CONFIG },
    { t: 'bundle', p: { hash: 'h1', manifest: manifest(presentations) } },
    { t: 'source', p: { playlistId: 'L', reason: 'default' } },
    { t: 'live', p: live },
  );
}

// ---------------------------------------------------------------- IP overlay

describe('IP overlay at start', () => {
  it('shows all addresses bottom right for 10–15 s, then hides itself', async () => {
    await push({ t: 'mode', p: MODE }, { t: 'config', p: CONFIG });
    const ip = $('ip');
    expect(visible('ip')).toBe(true);
    expect(ip.textContent).toContain('192.168.1.20');
    expect(ip.textContent).toContain('100.64.0.7');
    expect(ip.textContent).toContain('Web interface: http://192.168.1.20:8080');
    const cs = getComputedStyle(ip);
    expect(cs.position).toBe('fixed');
    expect(cs.right).not.toBe('');
    expect(cs.bottom).not.toBe('');
    expect(cs.left === '' || cs.left === 'auto').toBe(true);
    expect(cs.top === '' || cs.top === 'auto').toBe(true);

    await vi.advanceTimersByTimeAsync(10_000);
    expect(visible('ip')).toBe(true);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(visible('ip')).toBe(false);
  });

  it('is shown once per start, not again on later mode changes', async () => {
    await push({ t: 'mode', p: MODE }, { t: 'config', p: CONFIG });
    await vi.advanceTimersByTimeAsync(15_000);
    await push(
      { t: 'mode', p: { ...MODE, kind: 'blackout' } },
      { t: 'mode', p: MODE },
      { t: 'config', p: { ...CONFIG, scaleMode: 'cover' } },
    );
    expect(visible('ip')).toBe(false);
  });

  it('stays off when the overlay is disabled for the device', async () => {
    await push({ t: 'mode', p: MODE }, { t: 'config', p: { ...CONFIG, showIpOverlay: false } });
    expect(visible('ip')).toBe(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(visible('ip')).toBe(false);
  });

  it('system screens show the addresses themselves; the overlay follows when content starts', async () => {
    await push({ t: 'mode', p: { ...MODE, kind: 'pairing', pin: '123456' } }, { t: 'config', p: CONFIG });
    expect(visible('screen')).toBe(true);
    expect($('screen').textContent).toContain('192.168.1.20');
    expect(visible('ip')).toBe(false);
    await push({ t: 'mode', p: MODE });
    expect(visible('screen')).toBe(false);
    expect(visible('ip')).toBe(true);
  });
});

describe('network change waiting for its confirmation', () => {
  it('keeps the addresses of the WebGUI on screen until it is confirmed', async () => {
    await push({ t: 'mode', p: MODE }, { t: 'config', p: { ...CONFIG, showIpOverlay: false } });
    expect(visible('netnotice')).toBe(false);
    await push({ t: 'networkNotice', p: { text: 'New network setting\nhttps://10.20.3.200:8443' } });
    expect(visible('netnotice')).toBe(true);
    expect($('netnotice').textContent).toContain('https://10.20.3.200:8443');
    expect(getComputedStyle($('netnotice')).whiteSpace).toBe('pre-line');
    await vi.advanceTimersByTimeAsync(60_000); // no timeout: it stays
    expect(visible('netnotice')).toBe(true);
    await push({ t: 'networkNotice', p: { text: null } });
    expect(visible('netnotice')).toBe(false);
  });
});

// ---------------------------------------------------------------- scale mode and assets

describe('scale mode and assets on the player', () => {
  function stageRoot(): HTMLElement {
    return document.querySelector<HTMLElement>('.os-stage')!;
  }
  function factor(): number {
    const canvases = stageRoot().querySelectorAll<HTMLElement>(':scope > .os-canvas');
    return parseFloat(canvases[canvases.length - 1]!.style.getPropertyValue('--s'));
  }

  it('applies a new scale mode from the agent at once, without reloading the slide', async () => {
    setBox(stageRoot(), 1280, 800);
    ro.resize();
    const text: TextElement = {
      ...base('t'),
      type: 'text',
      doc: { paragraphs: [{ runs: [{ text: 'Hi' }] }] },
      style: TEXT_STYLE,
      valign: 'top',
    };
    await startPlaying([presentation('P', [slide('a', { elements: [text] })])]);
    const el = document.querySelector('[data-element-id="t"]');
    expect(el).not.toBeNull();
    expect(factor()).toBeCloseTo(1280 / 1920, 4);
    await push({ t: 'config', p: { ...CONFIG, scaleMode: 'cover' } });
    expect(factor()).toBeCloseTo(800 / 1080, 4);
    await push({ t: 'config', p: { ...CONFIG, scaleMode: 'none' } });
    expect(factor()).toBe(1);
    expect(document.querySelector('[data-element-id="t"]')).toBe(el); // same node: no reload
  });

  it('loads images in the variant the agent selected for this screen', async () => {
    setBox(stageRoot(), 1920, 1080);
    ro.resize();
    const img: ImageElement = {
      ...base('img', { w: 1280, h: 720 }),
      type: 'image',
      assetId: 'photo',
      fit: 'cover',
    };
    await startPlaying([
      presentation(
        'P',
        [
          slide('a', {
            elements: [img],
            background: { type: 'image', color: '#000000', assetId: 'wall', fit: 'cover' },
          }),
        ],
        1280,
        720,
      ),
    ]);
    expect(factor()).toBe(1.5);
    // /assets/{id} without a variant = the screen-sized variant cached by the agent
    // (i1920 up to 1920 px wide, the original above); thumbnails are never used here.
    expect(document.querySelector('[data-element-id="img"] img')!.getAttribute('src')).toBe('/assets/photo');
    expect(document.querySelector<HTMLElement>('.os-bg')!.style.backgroundImage).toBe('url("/assets/wall")');
  });
});

// ---------------------------------------------------------------- ticker overlay

describe('ticker overlay of the time schedule', () => {
  const zoneTickerElement: TickerElement = {
    ...base('tick', { y: 1000, w: 1920, h: 80 }),
    type: 'ticker',
    tickerSource: 'zone',
    style: TEXT_STYLE,
    speed: 100,
    direction: 'left',
  };
  const plain = (id: string): Slide => slide(id, { duration: { mode: 'fixed', ms: 10_000 } });

  function overlayText(): string[] {
    return [...$('zone-ticker').querySelectorAll('.os-ticker-track > span')].map((s) => s.textContent ?? '');
  }
  function overlayAnimations() {
    return anims.calls.filter((c) => $('zone-ticker').contains(c.el));
  }

  it('shows the ticker of the active rule over the content and updates it live', async () => {
    await startPlaying([presentation('P', [plain('a')])], liveState('Happy Hour'));
    expect(visible('zone-ticker')).toBe(true);
    expect(new Set(overlayText())).toEqual(new Set(['Happy Hour']));
    expect($('zone-ticker').style.bottom).toBe('0px');
    const [first] = overlayAnimations();
    expect(first!.keyframes[0]).toEqual({ transform: 'translateX(0px)' });

    await push({ t: 'live', p: liveState('Last orders') });
    expect(new Set(overlayText())).toEqual(new Set(['Last orders']));
    expect(first!.cancelled).toBe(true);
    const running = overlayAnimations().filter((c) => !c.cancelled);
    expect(running).toHaveLength(1);
    expect(running[0]!.keyframes[0]).toEqual({ transform: 'translateX(0px)' }); // on screen at once

    await push({ t: 'live', p: liveState('Last orders', false) });
    expect(visible('zone-ticker')).toBe(false);
  });

  it('keeps scrolling across slide changes and unrelated live updates', async () => {
    await startPlaying([presentation('P', [plain('a'), plain('b')])], liveState('Happy Hour'));
    const [a] = overlayAnimations();
    await vi.advanceTimersByTimeAsync(25_000); // a → b → a
    const msg = liveState('Happy Hour');
    msg.state.products[0]!.status = 'SOLD_OUT';
    await push({ t: 'live', p: msg });
    expect(a!.cancelled).toBe(false);
    expect(overlayAnimations()).toHaveLength(1);
  });

  it('a slide with a ticker element on "zone ticker" hides the overlay and shows the text itself', async () => {
    const withElement = slide('t', {
      duration: { mode: 'fixed', ms: 10_000 },
      elements: [zoneTickerElement],
    });
    await startPlaying([presentation('P', [plain('a'), withElement])], liveState('Happy Hour'));
    expect(visible('zone-ticker')).toBe(true);

    await vi.advanceTimersByTimeAsync(10_000);
    expect(visible('zone-ticker')).toBe(false); // no doubled text
    const el = document.querySelector<HTMLElement>('#stage [data-element-id="tick"]')!;
    expect(el.textContent).toContain('Happy Hour');

    await push({ t: 'live', p: liveState('Last orders') });
    expect(visible('zone-ticker')).toBe(false);
    expect(el.textContent).toContain('Last orders');
    expect(el.textContent).not.toContain('Happy Hour');

    await vi.advanceTimersByTimeAsync(10_000); // back to the slide without element
    expect(visible('zone-ticker')).toBe(true);
    expect(new Set(overlayText())).toEqual(new Set(['Last orders']));
  });

  it('ticker elements with their own text or hidden ones do not hide the overlay', async () => {
    const own = slide('own', { elements: [{ ...zoneTickerElement, tickerSource: 'text', text: 'Welcome' }] });
    const hidden = slide('hidden', { elements: [{ ...zoneTickerElement, hidden: true }] });
    await startPlaying([presentation('P', [own, hidden])], liveState('Happy Hour'));
    expect(visible('zone-ticker')).toBe(true);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(visible('zone-ticker')).toBe(true);
  });
});

// ---------------------------------------------------------------- plasma regeneration

describe('plasma regeneration', () => {
  it('shows only noise, stops the content and the ticker, and starts over when switched off', async () => {
    const put = vi.fn();
    const getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (
      kind: string,
    ) {
      if (kind !== '2d') return null;
      return {
        createImageData: (w: number, h: number) => ({
          width: w,
          height: h,
          data: new Uint8ClampedArray(w * h * 4),
        }),
        putImageData: put,
      } as unknown as CanvasRenderingContext2D;
    } as typeof HTMLCanvasElement.prototype.getContext);
    try {
      const s = (id: string): Slide =>
        slide(id, {
          duration: { mode: 'fixed', ms: 10_000 },
          elements: [
            {
              ...base(`t-${id}`),
              type: 'text',
              doc: { paragraphs: [{ runs: [{ text: `slide-${id}` }] }] },
              style: TEXT_STYLE,
              valign: 'top',
            },
          ],
        });
      const stageText = (): string => $('stage').textContent ?? '';
      await startPlaying([presentation('P', [s('a'), s('b')])], liveState('Happy Hour'));
      await vi.advanceTimersByTimeAsync(10_000); // second slide
      expect(stageText()).toContain('slide-b');
      expect(visible('zone-ticker')).toBe(true);
      await push({ t: 'versionNotice', p: { text: 'version' } });
      expect(visible('notice')).toBe(true);

      await push({ t: 'mode', p: { ...MODE, kind: 'regeneration' } });
      expect(visible('noise')).toBe(true);
      expect($('noise').querySelector('canvas')).not.toBeNull();
      expect(put).toHaveBeenCalled();
      expect(visible('zone-ticker')).toBe(false);
      expect(visible('screen')).toBe(false);
      expect(visible('notice')).toBe(false);
      expect(getComputedStyle($('noise')).zIndex).toBe('100'); // above system screens and notices
      expect(stageText()).not.toContain('slide-'); // content stopped

      await push({ t: 'mode', p: MODE });
      expect(visible('noise')).toBe(false);
      expect($('noise').querySelector('canvas')).toBeNull();
      expect(visible('zone-ticker')).toBe(true);
      expect(visible('notice')).toBe(true);
      await vi.advanceTimersByTimeAsync(0);
      expect(stageText()).toContain('slide-a'); // from the start
    } finally {
      getContext.mockRestore();
    }
  });
});
