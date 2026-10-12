import { describe, expect, it } from 'vitest';
import fixture from '../../../../testdata/contracts/presentation-valid.json';
import type { Presentation } from './index';

describe('shared contract fixtures', () => {
  it('presentation fixture matches the TS contract', () => {
    const p: Presentation = fixture as Presentation;
    expect(p.width).toBe(1280);
    const types = new Set(p.slides.flatMap((s) => s.elements.map((e) => e.type)));
    for (const t of ['text', 'image', 'productList', 'shape', 'clock', 'ticker', 'video']) {
      expect(types.has(t as never)).toBe(true);
    }
  });
});
