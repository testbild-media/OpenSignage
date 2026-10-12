import { describe, expect, it } from 'vitest';
import type { Presentation, ProductListElement, TextStyle } from '@opensignage/contracts';
import { clearRole, effectiveStyle, isOverridden, setOverride } from './overrides';
import { resizePresentation } from './store';

const base: TextStyle = { font: 'Montserrat', size: 40, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' };
const typo = { heading: { ...base, size: 60, bold: true }, subheading: base, item: base, description: { ...base, size: 24 }, price: base };

describe('product typography overrides', () => {
  it('set single properties and inherit the rest', () => {
    let o = setOverride(undefined, 'item', 'size', 52);
    o = setOverride(o, 'item', 'color', '#ff0000');
    o = setOverride(o, 'price', 'bold', true);
    expect(o).toEqual({ item: { size: 52, color: '#ff0000' }, price: { bold: true } });
    const item = effectiveStyle(typo, o, 'item');
    expect(item.size).toBe(52);
    expect(item.color).toBe('#ff0000');
    expect(item.font).toBe('Montserrat'); // inherited
    expect(effectiveStyle(typo, o, 'heading')).toEqual(typo.heading);
    expect(isOverridden(o, 'item')).toBe(true);
    expect(isOverridden(o, 'item', 'font')).toBe(false);
    expect(isOverridden(o, 'description')).toBe(false);
  });

  it('reset leaves no empty objects behind', () => {
    let o = setOverride(undefined, 'heading', 'italic', true);
    o = setOverride(o, 'heading', 'font', 'Lobster');
    o = setOverride(o, 'heading', 'italic', undefined);
    expect(o).toEqual({ heading: { font: 'Lobster' } });
    expect(setOverride(o, 'heading', 'font', undefined)).toBeUndefined();
    expect(clearRole({ heading: { bold: false }, price: { size: 10 } }, 'heading')).toEqual({ price: { size: 10 } });
    expect(clearRole({ price: { size: 10 } }, 'price')).toBeUndefined();
  });

  it('keeps false as a real override (not bold although the presentation is)', () => {
    const o = setOverride(undefined, 'heading', 'bold', false);
    expect(effectiveStyle(typo, o, 'heading').bold).toBe(false);
  });

  it('override sizes scale with the design resolution like the typography', () => {
    const list: ProductListElement = {
      id: 'l', type: 'productList', name: '', x: 0, y: 0, w: 960, h: 540, rotation: 0, opacity: 1, locked: false, hidden: false, pinned: false,
      source: { kind: 'all', ids: [] }, columns: 1, fields: { name: true, quantity: true, description: true, price: true }, scale: 1, priceAlign: 'right',
      overrides: { item: { size: 60, letterSpacing: 3, color: '#ff0000' } },
    };
    const d: Presentation = {
      id: 'p', name: 'P', width: 1920, height: 1080, typography: structuredClone(typo),
      slides: [{ id: 's', name: '', hidden: false, duration: { mode: 'fixed', ms: 10000 }, background: { type: 'color', color: '#000000' }, transition: { type: 'cut', ms: 0 }, elements: [list] }],
    };
    resizePresentation(d, 1280, 720);
    const o = (d.slides[0]!.elements[0] as ProductListElement).overrides!;
    expect(o.item!.size).toBeCloseTo(40);
    expect(o.item!.letterSpacing).toBeCloseTo(2);
    expect(o.item!.color).toBe('#ff0000');
  });
});
