import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LiveState, Playlist, Presentation, ProductListElement, Slide, TextStyle } from '@opensignage/contracts';
import { LiveStore } from './live';
import { renderElement } from './elements';
import { Sequencer, type SequencerContent } from './sequencer';
import type { Stage, StageSlide } from './stage';
import { elementSignature, backgroundSignature } from './stage';
import type { RenderEnv } from './env';

const style: TextStyle = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' };
const typography = { heading: style, subheading: style, item: style, description: style, price: style };

function live(): LiveState {
  return {
    zone: { id: 'z', name: 'Bar', state: 'OPEN' },
    ticker: { text: '', visible: false, template: { font: 'Montserrat', size: 40, color: '#fff', background: '#000', speed: 100, direction: 'left', height: 64, position: 'bottom' } },
    categories: [
      { id: 'beer', name: 'Beer', sort: 1 },
      { id: 'wine', name: 'Wine', sort: 2 },
    ],
    products: [
      { id: 'p1', name: 'Pils', description: '', categoryId: 'beer', type: 'drink', price: { amount: 450, currency: 'EUR' }, status: 'SOLD_OUT', showImageInsteadOfName: false, sort: 1 },
      { id: 'p2', name: 'Weizen', description: '', categoryId: 'beer', type: 'drink', price: { amount: 500, currency: 'EUR' }, status: 'AVAILABLE', showImageInsteadOfName: false, sort: 2 },
      { id: 'p3', name: 'Riesling', description: '', categoryId: 'wine', type: 'drink', price: { amount: 600, currency: 'EUR' }, status: 'UNAVAILABLE', showImageInsteadOfName: false, sort: 1 },
    ],
    serverTime: new Date().toISOString(),
  };
}

function env(store: LiveStore): RenderEnv {
  return { typography, assets: { url: () => null }, live: store, mode: 'play' };
}

const listEl: ProductListElement = {
  id: 'l', type: 'productList', name: '', x: 0, y: 0, w: 800, h: 600, rotation: 0, opacity: 1, locked: false, hidden: false, pinned: false,
  source: { kind: 'all', ids: [] }, columns: 1, fields: { name: true, quantity: true, description: true, price: true },
  categoryHeadings: true, subcategoryHeadings: true, scale: 1, priceAlign: 'right',
};

describe('product list', () => {
  it('keeps three product states distinct', () => {
    const store = new LiveStore();
    store.language = 'de';
    store.set(live());
    const h = renderElement(listEl, env(store));
    const rows = [...h.el.querySelectorAll<HTMLElement>('.os-row')];
    expect(rows.map((r) => r.dataset.id)).toEqual(['p1', 'p2']); // UNAVAILABLE hidden, no gap
    expect(rows[0]!.classList.contains('os-soldout')).toBe(true); // SOLD_OUT shown with bar
    expect(rows[1]!.classList.contains('os-soldout')).toBe(false);
    // The heading is never greyed; the wine category disappears entirely.
    const headings = [...h.el.querySelectorAll('.os-pl-h')].map((x) => x.textContent);
    expect(headings).toEqual(['Beer']);
    expect(h.el.querySelector('.os-pl-h')!.classList.contains('os-soldout')).toBe(false);
  });

  it('patches only the changed row and applies schedule prices', () => {
    const store = new LiveStore();
    store.set(live());
    const h = renderElement(listEl, env(store));
    const before = h.el.querySelector('[data-id="p2"]');
    const state = live();
    state.products[0]!.status = 'AVAILABLE';
    store.set(state, { p2: { amount: 300, currency: 'EUR' } });
    const after = h.el.querySelector('[data-id="p2"]')!;
    expect(after).not.toBe(before); // price changed → row replaced
    expect(after.textContent).toContain('3');
    expect(h.el.querySelector('[data-id="p1"]')!.classList.contains('os-soldout')).toBe(false);
  });
});

describe('signatures', () => {
  it('identical pinned elements and backgrounds match', () => {
    const a = { ...listEl, id: 'a', pinned: true };
    const b = { ...listEl, id: 'b', pinned: true, name: 'other name' };
    expect(elementSignature(a)).toBe(elementSignature(b));
    expect(elementSignature({ ...b, x: 5 })).not.toBe(elementSignature(a));
    expect(backgroundSignature({ type: 'video', color: '#000000', assetId: 'v', fit: 'cover' })).toBe(
      backgroundSignature({ type: 'video', color: '#000000', assetId: 'v', fit: 'cover' }),
    );
  });
});

function slide(id: string, ms: number): Slide {
  return { id, name: id, hidden: false, duration: { mode: 'fixed', ms }, background: { type: 'color', color: '#000000' }, transition: { type: 'cut', ms: 0 }, elements: [] };
}

class FakeStage {
  shown: string[] = [];
  root = document.createElement('div');
  events: { onBackgroundEnded?: () => void; onMediaFailed?: (r: string) => void } = {};
  async show(s: StageSlide): Promise<void> {
    this.shown.push(s.slide.id);
  }
  clear(): void {
    this.shown.push('CLEAR');
  }
}

describe('sequencer', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('plays repeat counts exactly and durations without cutting a pass', async () => {
    const pres: Presentation = { id: 'P', name: 'P', width: 1920, height: 1080, typography, slides: [slide('a', 2000), slide('b', 2000)] };
    const pl: Playlist = {
      id: 'L', name: 'L', items: [
        { id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 2 } },
        { id: 'i2', kind: 'slide', refId: 'b', presentationId: 'P', play: { mode: 'duration', ms: 5000 } },
      ],
    };
    const content: SequencerContent = { presentations: new Map([['P', pres]]), playlists: new Map([['L', pl]]), media: new Map() };
    const stage = new FakeStage();
    const seq = new Sequencer(stage as unknown as Stage, () => env(new LiveStore()));
    seq.setContent(content);
    seq.play('L');
    await vi.advanceTimersByTimeAsync(2000 * 4 + 2000 * 3 + 100);
    // 2 passes of a,b then slide b until ≥5 s → 3 passes (6 s, the 3rd pass is finished)
    expect(stage.shown.filter((x) => x !== 'CLEAR').slice(0, 7)).toEqual(['a', 'b', 'a', 'b', 'b', 'b', 'b']);
    seq.play(null);
    expect(stage.shown.at(-1)).toBe('CLEAR');
  });

  it('a long slide is not reset by new identical play() calls', async () => {
    const pres: Presentation = { id: 'P', name: 'P', width: 1920, height: 1080, typography, slides: [slide('a', 20000), slide('b', 1000)] };
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    const stage = new FakeStage();
    const seq = new Sequencer(stage as unknown as Stage, () => env(new LiveStore()));
    seq.setContent({ presentations: new Map([['P', pres]]), playlists: new Map([['L', pl]]), media: new Map() });
    seq.play('L');
    for (let i = 0; i < 5; i++) {
      await vi.advanceTimersByTimeAsync(4000);
      seq.play('L'); // e.g. a schedule re-evaluation every minute must not restart
    }
    await vi.advanceTimersByTimeAsync(1000);
    expect(stage.shown.filter((x) => x !== 'CLEAR').slice(0, 2)).toEqual(['a', 'b']);
  });
});
