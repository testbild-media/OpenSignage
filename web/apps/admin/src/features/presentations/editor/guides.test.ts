import { afterEach, describe, expect, it } from 'vitest';
import type { Presentation, ShapeElement } from '@opensignage/contracts';
import { dropGuide, loadGuides, nearest, saveGuides, snapCandidates } from './guides';
import { EditorStore } from './store';
import { rulerMinor, rulerStep } from './Rulers';

const style = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' as const };

function shape(id: string, x: number): ShapeElement {
  return { id, type: 'shape', name: '', x, y: 10, w: 100, h: 50, rotation: 0, opacity: 1, locked: false, hidden: false, pinned: false, shape: 'rect', stroke: { color: '#ffffff', width: 2, dash: 'solid' } };
}

function doc(): Presentation {
  return {
    id: 'p1',
    name: 'P',
    width: 1920,
    height: 1080,
    typography: { heading: style, subheading: style, item: style, description: style, price: style },
    slides: [{ id: 's1', name: '', hidden: false, duration: { mode: 'fixed', ms: 10000 }, background: { type: 'color', color: '#000000' }, transition: { type: 'fade', ms: 500 }, elements: [shape('a', 0), shape('b', 400)] }],
  };
}

afterEach(() => localStorage.clear());

describe('guides', () => {
  it('are kept per presentation in this browser', () => {
    const s = new EditorStore(doc(), 'pres-1');
    s.addGuide('x', 300);
    s.addGuide('y', 540);
    s.moveGuide('x', 0, 320);
    expect(s.getSnapshot().guides).toEqual({ x: [320], y: [540] });
    // A new editor for the same presentation sees them, another presentation does not.
    expect(new EditorStore(doc(), 'pres-1').guides).toEqual({ x: [320], y: [540] });
    expect(new EditorStore(doc(), 'pres-2').guides).toEqual({ x: [], y: [] });
    s.removeGuide('y', 0);
    expect(loadGuides('pres-1')).toEqual({ x: [320], y: [] });
    s.clearGuides();
    expect(localStorage.getItem('os.editor.guides.pres-1')).toBeNull();
  });

  it('are not part of the document and not in the undo history', () => {
    const s = new EditorStore(doc(), 'pres-1');
    s.addGuide('x', 100);
    expect(s.getSnapshot().canUndo).toBe(false);
    expect(s.dirty).toBe(false);
    expect(JSON.stringify(s.presentation)).not.toContain('guide');
  });

  it('ignore broken stored data', () => {
    localStorage.setItem('os.editor.guides.bad', '{"x":[1,"a",null,3],"y":"no"}');
    expect(loadGuides('bad')).toEqual({ x: [1, 3], y: [] });
    localStorage.setItem('os.editor.guides.worse', 'not json');
    expect(loadGuides('worse')).toEqual({ x: [], y: [] });
    saveGuides('ok', { x: [5], y: [] });
    expect(loadGuides('ok')).toEqual({ x: [5], y: [] });
  });

  it('are removed when dropped on a ruler or off the slide', () => {
    expect(dropGuide(100.4, 1920, false)).toBe(100);
    expect(dropGuide(100, 1920, true)).toBeNull();
    expect(dropGuide(-1, 1920, false)).toBeNull();
    expect(dropGuide(1921, 1920, false)).toBeNull();
    expect(dropGuide(1920, 1920, false)).toBe(1920);
  });

  it('are snap targets next to slide and element edges', () => {
    const d = doc();
    const c = snapCandidates(d.slides[0]!, d.width, d.height, new Set(['a']), { x: [777], y: [333] });
    expect(c.xs).toContain(777);
    expect(c.ys).toContain(333);
    expect(c.xs).toContain(400); // element b
    expect(c.xs).not.toContain(100); // right edge of the moved element a itself
    // An element whose left edge is 4 px from a guide snaps onto it.
    expect(nearest([773, 823, 873], c.xs, 6)).toEqual({ delta: 4, at: 777 });
    expect(nearest([700], [777], 6)).toBeNull();
  });
});

describe('rulers', () => {
  it('pick readable tick steps for every zoom', () => {
    expect(rulerStep(1)).toBe(100);
    expect(rulerStep(0.4)).toBe(200);
    expect(rulerStep(4)).toBe(20);
    expect(rulerMinor(100, 1)).toBe(10);
    expect(rulerMinor(200, 0.4)).toBe(20);
    expect(rulerMinor(25, 4)).toBe(5);
  });
});
