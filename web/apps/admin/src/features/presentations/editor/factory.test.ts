import { describe, expect, it } from 'vitest';
import type { Presentation } from '@opensignage/contracts';
import {
  FACTORY_DEFAULTS,
  contentDefaults,
  createElement,
  createSlide,
  type PresentationDefaults,
} from './factory';

const doc = { id: 'p', name: 'P', width: 1920, height: 1080, slides: [] } as unknown as Presentation;

const presentations: PresentationDefaults = {
  width: 1280,
  height: 720,
  background: '#112233',
  transition: { type: 'slide', ms: 800, direction: 'up' },
  text: { font: 'Bebas Neue', color: '#ff0000' },
  typography: {} as PresentationDefaults['typography'],
};

describe('start values from Settings → Defaults', () => {
  it('uses the factory values until the settings are loaded', () => {
    expect(contentDefaults(undefined)).toEqual(FACTORY_DEFAULTS);
    const s = createSlide();
    expect(s.duration).toEqual({ mode: 'fixed', ms: 10_000 });
    expect(s.background).toEqual({ type: 'color', color: '#000000' });
    expect(s.transition).toEqual({ type: 'fade', ms: 600 });
  });

  it('gives new slides duration, background and transition of the settings', () => {
    const d = contentDefaults({ defaults: { slideMs: 7000 }, presentations });
    const s = createSlide(d);
    expect(s.duration).toEqual({ mode: 'fixed', ms: 7000 });
    expect(s.background).toEqual({ type: 'color', color: '#112233' });
    expect(s.transition).toEqual({ type: 'slide', ms: 800, direction: 'up' });
    // a copy: changing the slide never changes the defaults
    s.transition.ms = 100;
    expect(d.transition.ms).toBe(800);
  });

  it('gives new texts, clocks and tickers the font and color of the settings', () => {
    const d = contentDefaults({ defaults: { slideMs: 7000 }, presentations });
    for (const kind of ['text', 'clock', 'ticker'] as const) {
      const e = createElement(kind, doc, { defaults: d });
      if (e.type !== 'text' && e.type !== 'clock' && e.type !== 'ticker') throw new Error(e.type);
      expect(e.style.font).toBe('Bebas Neue');
      expect(e.style.color).toBe('#ff0000');
    }
    const plain = createElement('text', doc);
    if (plain.type !== 'text') throw new Error(plain.type);
    expect(plain.style.font).toBe('Montserrat');
  });
});
