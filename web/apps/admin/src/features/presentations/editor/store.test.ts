import { describe, expect, it } from 'vitest';
import type { AudioElement, Presentation, ShapeElement, Slide, VideoElement } from '@opensignage/contracts';
import { EditorStore, mediaLimit, resizePresentation, type MediaLimit } from './store';

const style = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' as const };

function shape(id: string, x: number): ShapeElement {
  return { id, type: 'shape', name: '', x, y: 10, w: 100, h: 50, rotation: 0, opacity: 1, locked: false, hidden: false, pinned: false, shape: 'rect', stroke: { color: '#ffffff', width: 2, dash: 'solid' } };
}

function doc(): Presentation {
  return {
    id: 'p', name: 'P', width: 1920, height: 1080,
    typography: { heading: style, subheading: style, item: style, description: style, price: style },
    slides: [{ id: 's1', name: '', hidden: false, duration: { mode: 'fixed', ms: 10000 }, background: { type: 'color', color: '#000000' }, transition: { type: 'fade', ms: 500 }, elements: [shape('a', 0), shape('b', 200), shape('c', 600)] }],
  };
}

describe('EditorStore', () => {
  it('panel focus: presentation when nothing is selected, the slide, or the elements', () => {
    const s = new EditorStore(doc());
    const level = (): string => (s.getSnapshot().selection.length ? 'element' : s.getSnapshot().focus);
    expect(level()).toBe('presentation'); // opened: nothing selected
    s.selectSlide('s1'); // thumbnail of the slide that is already shown
    expect(level()).toBe('slide');
    s.select(['a']);
    expect(level()).toBe('element');
    s.escape(); // Esc: element → its slide → the presentation
    expect(level()).toBe('slide');
    s.escape();
    expect(level()).toBe('presentation');
    s.select(['b']);
    s.focusPresentation(); // click beside the slide
    expect(level()).toBe('presentation');
    expect(s.getSnapshot().selection).toEqual([]);
    s.clearSelection(); // click on an empty spot of the slide
    expect(level()).toBe('slide');
  });

  it('undo/redo and interactions are single steps', () => {
    const s = new EditorStore(doc());
    s.select(['a']);
    s.begin();
    for (let i = 1; i <= 5; i++) s.preview((d) => (d.slides[0]!.elements[0]!.x = i * 10));
    s.commit();
    expect(s.presentation.slides[0]!.elements[0]!.x).toBe(50);
    s.undo();
    expect(s.presentation.slides[0]!.elements[0]!.x).toBe(0);
    s.redo();
    expect(s.presentation.slides[0]!.elements[0]!.x).toBe(50);
  });

  it('arrange gives a unique order and moves the right elements', () => {
    const s = new EditorStore(doc());
    s.select(['a']);
    s.arrange('front');
    expect(s.presentation.slides[0]!.elements.map((e) => e.id)).toEqual(['b', 'c', 'a']);
    s.arrange('backward');
    expect(s.presentation.slides[0]!.elements.map((e) => e.id)).toEqual(['b', 'a', 'c']);
  });

  it('groups select together, align and distribute', () => {
    const s = new EditorStore(doc());
    s.select(['a', 'b']);
    s.group();
    s.clearSelection();
    s.select(['a']);
    expect(s.getSnapshot().selection).toEqual(['a', 'b']);
    s.select(['a', 'b', 'c']);
    s.align('top');
    expect(s.presentation.slides[0]!.elements.every((e) => e.y === 10)).toBe(true);
    s.distribute('h');
    const xs = s.presentation.slides[0]!.elements.map((e) => e.x);
    expect(xs).toEqual([0, 300, 600]);
  });

  it('paste creates new ids and duplicates keep groups separate', () => {
    const s = new EditorStore(doc());
    s.select(['a', 'b']);
    s.group();
    s.duplicate();
    const els = s.presentation.slides[0]!.elements;
    expect(els).toHaveLength(5);
    expect(new Set(els.map((e) => e.id)).size).toBe(5);
    expect(els[3]!.groupId).toBeDefined();
    expect(els[3]!.groupId).not.toBe(els[0]!.groupId);
  });

  it('resize scales positions per axis and sizes uniformly', () => {
    const d = doc();
    resizePresentation(d, 1280, 720);
    const a = d.slides[0]!.elements[0]!;
    expect(d.width).toBe(1280);
    expect(a.w).toBeCloseTo(100 * (2 / 3));
    expect(d.typography.item.size).toBeCloseTo(16);
  });

  it('paste, duplicate and copies to other slides keep the media limits (one video, one audio per slide)', () => {
    const video = (id: string): VideoElement => ({ ...shape(id, 0), type: 'video', assetId: 'v', fit: 'contain' }) as unknown as VideoElement;
    const audio = (id: string): AudioElement => ({ ...shape(id, 0), type: 'audio', assetId: 'a', volume: 1 }) as unknown as AudioElement;
    const d = doc();
    const s2: Slide = { ...structuredClone(d.slides[0]!), id: 's2', elements: [video('v2')] };
    const s3: Slide = { ...structuredClone(d.slides[0]!), id: 's3', elements: [] };
    d.slides[0]!.elements.push(video('v1'), audio('a1'));
    d.slides.push(s2, s3);
    const s = new EditorStore(d);
    const refused: MediaLimit[] = [];
    s.onLimit = (k) => refused.push(k);
    const count = (slide: number, type: string): number => s.presentation.slides[slide]!.elements.filter((e) => e.type === type).length;

    // Duplicate on the same slide: a second video is refused, nothing changes.
    s.select(['v1']);
    s.duplicate();
    expect(refused).toEqual(['video']);
    expect(count(0, 'video')).toBe(1);
    expect(s.getSnapshot().canUndo).toBe(false);

    // Copy + paste on the same slide or on a slide that already has a video: refused.
    s.copy();
    s.paste();
    s.selectSlide('s2');
    s.paste();
    expect(refused).toEqual(['video', 'video', 'video']);
    expect(count(1, 'video')).toBe(1);
    // Onto a slide without video: allowed.
    s.selectSlide('s3');
    s.paste();
    expect(count(2, 'video')).toBe(1);

    // Audio follows the same rule.
    s.selectSlide('s1');
    s.select(['a1']);
    s.copy();
    s.paste();
    expect(refused.at(-1)).toBe('audio');
    expect(count(0, 'audio')).toBe(1);

    // "To all slides" with a video is refused as a whole when another slide has one.
    s.select(['v1']);
    s.copyToAllSlides();
    expect(refused.at(-1)).toBe('video');
    expect(count(1, 'video')).toBe(1);
    expect(count(2, 'video')).toBe(1);

    // Inserting through the store is checked as well; other elements are not affected.
    expect(s.addElement(video('v9'))).toBe(false);
    s.select(['a']);
    s.duplicate();
    expect(s.presentation.slides[0]!.elements.filter((e) => e.type === 'shape')).toHaveLength(4);
    // Duplicating a valid slide is fine.
    s.duplicateSlide('s1');
    expect(s.presentation.slides).toHaveLength(4);
  });

  it('mediaLimit counts existing and added elements', () => {
    expect(mediaLimit([], [{ type: 'video' }])).toBeNull();
    expect(mediaLimit([{ type: 'video' }], [{ type: 'video' }])).toBe('video');
    expect(mediaLimit([], [{ type: 'video' }, { type: 'video' }])).toBe('video');
    expect(mediaLimit([{ type: 'video' }], [{ type: 'shape' }])).toBeNull();
    expect(mediaLimit([{ type: 'audio' }], [{ type: 'audio' }])).toBe('audio');
  });
});
