// Ticker: the text changes on screen through the live state, without a
// blank restart; speed and direction are honoured. The zone overlay itself is tested in
// apps/player (main.test.ts).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LiveState, ScheduleTicker, TickerElement } from '@opensignage/contracts';
import { createTicker } from './ticker';
import { renderElement } from './elements';
import { LiveStore } from './live';
import { TEXT_STYLE, base, fakeAnimations, fakeLayout, testEnv, type FakeAnimations } from './testing';

const CHAR_W = 10; // px per character of the fake text metrics
const HOST_W = 400;

let anims: FakeAnimations;
let restoreLayout: () => void;

beforeEach(() => {
  vi.useFakeTimers();
  anims = fakeAnimations();
  restoreLayout = fakeLayout((el) => {
    if (el.tagName === 'SPAN' && el.parentElement?.classList.contains('os-ticker-track')) {
      return { width: (el.textContent ?? '').length * CHAR_W, height: 30 };
    }
    if (el.classList.contains('os-ticker')) return { width: HOST_W, height: 40 };
    return null;
  });
  document.body.replaceChildren();
});

afterEach(() => {
  restoreLayout();
  anims.restore();
  vi.useRealTimers();
});

function track(host: HTMLElement): HTMLElement {
  return host.querySelector<HTMLElement>('.os-ticker-track')!;
}

function copies(host: HTMLElement): string[] {
  return [...track(host).children].map((c) => c.textContent ?? '');
}

/** Running (not cancelled, infinite) animations on the ticker track of host. */
function running(host: HTMLElement) {
  return anims.on(track(host)).filter((c) => !c.cancelled);
}

/** Ticker of the active time schedule rule as the agent sends it. */
function sched(text: string): ScheduleTicker {
  return {
    text,
    ruleId: 'r-happy',
    template: { font: 'Montserrat', size: 40, color: '#ffffff', background: '#000000', speed: 120, direction: 'left', height: 64, position: 'bottom' },
  };
}

function liveState(text: string, visible = true): LiveState {
  return {
    zone: { id: 'z', name: 'Bar', state: 'OPEN' },
    ticker: {
      text,
      visible,
      template: {
        font: 'Montserrat',
        size: 40,
        color: '#ffffff',
        background: '#000000',
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
    serverTime: new Date().toISOString(),
  };
}

function tickerElement(patch: Partial<TickerElement> = {}): TickerElement {
  return {
    ...base('tick', { x: 0, y: 1000, w: 1920, h: 80 }),
    type: 'ticker',
    tickerSource: 'zone',
    style: TEXT_STYLE,
    speed: 100,
    direction: 'left',
    ...patch,
  };
}

describe('marquee', () => {
  it('scrolls left at speed × stage factor, seamlessly', () => {
    const host = document.createElement('div');
    host.style.setProperty('--s', '1.5');
    document.body.appendChild(host);
    const t = createTicker(host, TEXT_STYLE, 100, 'left', true);
    t.setText('Happy Hour'); // 10 chars → one copy is 100 px wide
    t.start();
    const [a, ...more] = running(host);
    expect(more).toEqual([]);
    expect(a!.keyframes).toEqual([{ transform: 'translateX(0px)' }, { transform: 'translateX(-100px)' }]);
    expect(a!.options).toMatchObject({ iterations: Infinity, easing: 'linear' });
    expect(a!.options.duration).toBeCloseTo((100 / (100 * 1.5)) * 1000, 3); // 100 design px/s at factor 1.5
    // Enough copies for twice the visible width: moving by one copy loops without a gap.
    expect(copies(host).length * 100).toBeGreaterThanOrEqual(2 * HOST_W);
    expect(new Set(copies(host))).toEqual(new Set(['Happy Hour']));
  });

  it('scrolls right when configured', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const t = createTicker(host, TEXT_STYLE, 50, 'right', true);
    t.setText('0123456789012345678901234567890123456789'); // 400 px
    t.start();
    const [a] = running(host);
    expect(a!.keyframes).toEqual([{ transform: 'translateX(-400px)' }, { transform: 'translateX(0px)' }]);
    expect(a!.options.duration).toBe((400 / 50) * 1000);
    expect(copies(host)).toHaveLength(2);
  });

  it('starting an already running ticker keeps its position', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const t = createTicker(host, TEXT_STYLE, 100, 'left', true);
    t.setText('Happy Hour');
    t.start();
    const [first] = running(host);
    t.start();
    t.setText('Happy Hour');
    expect(first!.cancelled).toBe(false);
    expect(anims.on(track(host))).toHaveLength(1);
    // Stopped and started again (e.g. overlay hidden and shown) → runs again.
    t.stop();
    expect(first!.cancelled).toBe(true);
    t.start();
    expect(running(host)).toHaveLength(1);
  });

  it('shows static text in the editor (no animation)', () => {
    const host = document.createElement('div');
    const t = createTicker(host, TEXT_STYLE, 100, 'left', false);
    t.setText('Preview');
    t.start();
    expect(anims.calls).toEqual([]);
    expect(copies(host)).toEqual(['Preview']);
  });
});

describe('ticker element with source "ticker of the time schedule"', () => {
  it('a new ticker text of the active rule is on screen at once, not after a blank run-in', () => {
    const live = new LiveStore();
    live.set(liveState(''), {}, sched('Happy Hour'));
    const h = renderElement(tickerElement(), testEnv(live));
    document.body.appendChild(h.el);
    h.start();
    expect(copies(h.el)[0]).toBe('Happy Hour');
    const [before] = running(h.el);

    live.set(liveState(''), {}, sched('Last orders at 2 am')); // 19 chars → 190 px per copy
    // Synchronously replaced (no reload of the slide, same element node).
    expect(h.el.isConnected).toBe(true);
    expect(new Set(copies(h.el))).toEqual(new Set(['Last orders at 2 am']));
    expect(before!.cancelled).toBe(true);
    const [after, ...more] = running(h.el);
    expect(more).toEqual([]);
    // The new text starts at the left edge of the box (visible immediately) and the copies
    // cover the box for the whole loop: there is never an empty ticker.
    expect(after!.keyframes[0]).toEqual({ transform: 'translateX(0px)' });
    expect(after!.keyframes[1]).toEqual({ transform: 'translateX(-190px)' });
    expect(copies(h.el).length * 190 - 190).toBeGreaterThanOrEqual(HOST_W);
  });

  it('other live changes (prices, product status) do not restart the scroll', () => {
    const live = new LiveStore();
    live.set(liveState(''), {}, sched('Happy Hour'));
    const h = renderElement(tickerElement(), testEnv(live));
    document.body.appendChild(h.el);
    h.start();
    const [a] = running(h.el);
    const state = liveState('Happy Hour');
    state.products[0]!.status = 'SOLD_OUT';
    live.set(state, { p1: { amount: 300, currency: 'EUR' } }, sched('Happy Hour'));
    expect(a!.cancelled).toBe(false);
    expect(anims.on(track(h.el))).toHaveLength(1);
  });

  it('honours the element speed and direction', () => {
    const live = new LiveStore();
    live.set(liveState(''), {}, sched('Happy Hour'));
    const h = renderElement(tickerElement({ speed: 25, direction: 'right' }), testEnv(live));
    document.body.appendChild(h.el);
    h.start();
    const [a] = running(h.el);
    expect(a!.keyframes).toEqual([{ transform: 'translateX(-100px)' }, { transform: 'translateX(0px)' }]);
    expect(a!.options.duration).toBe((100 / 25) * 1000);
  });

  it('disappears on screens while no rule shows a ticker, and comes back', () => {
    const live = new LiveStore();
    live.set(liveState(''), {}, sched('Happy Hour'));
    const h = renderElement(tickerElement(), testEnv(live));
    document.body.appendChild(h.el);
    h.start();
    expect(h.el.style.visibility).toBe('');
    live.set(liveState(''), {}, null);
    expect(h.el.style.visibility).toBe('hidden');
    live.set(liveState(''), {}, sched('   '));
    expect(h.el.style.visibility).toBe('hidden');
    live.set(liveState(''), {}, sched('Back again'));
    expect(h.el.style.visibility).toBe('');
    // The editor keeps showing the element for layout work.
    const ed = renderElement(tickerElement(), { ...testEnv(live), mode: 'edit' });
    live.set(liveState(''), {}, null);
    expect(ed.el.style.visibility).toBe('');
  });

  it('an element with its own text ignores the time schedule ticker', () => {
    const live = new LiveStore();
    live.set(liveState(''), {}, sched('Happy Hour'));
    const h = renderElement(tickerElement({ tickerSource: 'text', text: 'Welcome' }), testEnv(live));
    document.body.appendChild(h.el);
    h.start();
    live.set(liveState(''), {}, sched('Changed'));
    expect(new Set(copies(h.el))).toEqual(new Set(['Welcome']));
  });
});
