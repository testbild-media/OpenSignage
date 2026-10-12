// Editing canvas: the slide is drawn by @opensignage/render (edit mode) exactly like the
// player; an overlay adds selection, move/resize/rotate handles, marquee, snapping, guide
// lines and the context menu. Rulers sit above and left of the scroll area.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { SlideElement } from '@opensignage/contracts';
import { backgroundSignature, color, injectRenderCss, renderElement, type ElementHandle, type RenderEnv } from '@opensignage/render';
import { useT } from '../../../lib/hooks';
import { bounds, type EditorStore } from './store';
import { useEditor } from './useEditor';
import { TextEditor } from './TextEditor';
import { dropGuide, nearest, snapCandidates, type GuideAxis } from './guides';
import { Ruler } from './Rulers';
import { ContextMenu, type MenuState } from './ContextMenu';
import { canvasMenu, elementMenu, guideMenu } from './menus';
import { SlideBackgroundDialog } from './Panel';

/** A snap line shown while moving or resizing. */
interface SnapLine {
  axis: GuideAxis;
  pos: number; // design px
}

interface GuideDrag {
  axis: GuideAxis;
  /** Index of the moved guide, null for a new guide pulled out of a ruler. */
  index: number | null;
  pos: number;
  /** Dropping now removes the guide (over a ruler or off the slide). */
  out: boolean;
}

type Drag =
  | { kind: 'move'; startX: number; startY: number; origin: Map<string, { x: number; y: number }> }
  | { kind: 'resize'; handle: string; startX: number; startY: number; origin: { x: number; y: number; w: number; h: number }; id: string }
  | { kind: 'rotate'; id: string; cx: number; cy: number }
  | { kind: 'marquee'; startX: number; startY: number; x: number; y: number };

const HANDLES = ['nw', 'n', 'ne', 'e', 'se', 's', 'sw', 'w'] as const;
const SNAP_PX = 6; // screen pixels

function isEditable(t: EventTarget | null): boolean {
  return t instanceof Element && !!t.closest('input, textarea, select, [contenteditable=true]');
}

export function Canvas({ store, env }: { store: EditorStore; env: RenderEnv }) {
  const t = useT();
  const snap = useEditor(store);
  const { doc, view } = snap;
  const slide = doc.slides.find((s) => s.id === snap.slideId);
  const outerRef = useRef<HTMLDivElement>(null);
  const slideRef = useRef<HTMLDivElement>(null);
  const layerRef = useRef<HTMLDivElement>(null);
  const bgRef = useRef<HTMLDivElement>(null);
  const rulerXRef = useRef<HTMLCanvasElement>(null);
  const rulerYRef = useRef<HTMLCanvasElement>(null);
  const handles = useRef(new Map<string, { json: string; h: ElementHandle }>());
  const [box, setBox] = useState({ w: 800, h: 450 });
  const [drag, setDrag] = useState<Drag | null>(null);
  const [snapLines, setSnapLines] = useState<SnapLine[]>([]);
  const [editingText, setEditingText] = useState<string | null>(null);
  const [guideDrag, setGuideDrag] = useState<GuideDrag | null>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);
  const [bgDialog, setBgDialog] = useState(false);

  useLayoutEffect(() => {
    injectRenderCss();
    const el = outerRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBox({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const scale = useMemo(() => {
    if (view.zoom === 'fit') return Math.max(0.05, Math.min((box.w - 48) / doc.width, (box.h - 48) / doc.height));
    return view.zoom;
  }, [view.zoom, box, doc.width, doc.height]);

  // Render elements; only elements whose JSON changed are rebuilt.
  useEffect(() => {
    const layer = layerRef.current;
    if (!layer || !slide) return;
    const seen = new Set<string>();
    let prev: HTMLElement | null = null;
    for (const e of slide.elements) {
      seen.add(e.id);
      const json = JSON.stringify(e);
      let entry = handles.current.get(e.id);
      if (!entry || entry.json !== json) {
        entry?.h.destroy();
        const h = renderElement(e, env);
        h.el.style.pointerEvents = 'none';
        if (entry) entry.h.el.replaceWith(h.el);
        entry = { json, h };
        handles.current.set(e.id, entry);
        h.start();
      }
      entry.h.el.style.visibility = editingText === e.id ? 'hidden' : '';
      // Keep DOM order = z-order.
      const node = entry.h.el;
      const expected: ChildNode | null = prev ? prev.nextSibling : layer.firstChild;
      if (expected !== node) layer.insertBefore(node, expected);
      prev = node;
    }
    for (const [id, entry] of handles.current) {
      if (!seen.has(id)) {
        entry.h.destroy();
        entry.h.el.remove();
        handles.current.delete(id);
      }
    }
  }, [slide, env, editingText]);

  // Destroy everything when the slide changes or on unmount.
  useEffect(() => {
    const map = handles.current;
    return () => {
      for (const e of map.values()) {
        e.h.destroy();
        e.h.el.remove();
      }
      map.clear();
    };
  }, [snap.slideId]);

  // Background (same visuals as the player, video as poster).
  useEffect(() => {
    const bg = bgRef.current;
    if (!bg || !slide) return;
    const b = slide.background;
    bg.style.backgroundColor = color(b.color, '#000');
    bg.style.backgroundImage = '';
    bg.dataset.sig = backgroundSignature(b);
    if (b.type === 'gradient') {
      bg.style.backgroundImage = `linear-gradient(${b.angle}deg, ${b.stops.map((s) => `${color(s.color)} ${s.at * 100}%`).join(', ')})`;
    } else if (b.type === 'image' || b.type === 'video') {
      const url = env.assets.url(b.assetId, b.type === 'video' ? 'poster' : 'display');
      if (url) bg.style.backgroundImage = `url("${url}")`;
      bg.style.backgroundSize = b.fit === 'fill' ? '100% 100%' : b.fit;
    }
  }, [slide, env]);

  const toDesign = useCallback(
    (clientX: number, clientY: number): { x: number; y: number } => {
      const r = layerRef.current!.getBoundingClientRect();
      return { x: (clientX - r.left) / scale, y: (clientY - r.top) / scale };
    },
    [scale],
  );

  /** Topmost element at a point; locked elements only for the context menu (to unlock them). */
  const hit = (x: number, y: number, withLocked = false): SlideElement | undefined => {
    if (!slide) return undefined;
    for (let i = slide.elements.length - 1; i >= 0; i--) {
      const e = slide.elements[i]!;
      if (e.hidden || (e.locked && !withLocked)) continue;
      if (x >= e.x && x <= e.x + e.w && y >= e.y && y <= e.y + e.h) return e;
    }
    return undefined;
  };

  // ---- guide lines ------------------------------------------------------------
  const overRuler = (cx: number, cy: number): boolean =>
    [rulerXRef.current, rulerYRef.current].some((r) => {
      if (!r) return false;
      const b = r.getBoundingClientRect();
      return cx >= b.left && cx <= b.right && cy >= b.top && cy <= b.bottom;
    });

  /** Drags a new guide out of a ruler (index null) or moves an existing one. */
  const startGuideDrag = (axis: GuideAxis, index: number | null, ev: React.PointerEvent): void => {
    ev.preventDefault();
    ev.stopPropagation();
    const size = axis === 'x' ? doc.width : doc.height;
    const { snap: snapOn, grid, gridSize } = view;
    const at = (e: { clientX: number; clientY: number; altKey: boolean }): GuideDrag => {
      const p = toDesign(e.clientX, e.clientY);
      let pos = axis === 'x' ? p.x : p.y;
      if (snapOn && grid && !e.altKey) pos = Math.round(pos / gridSize) * gridSize;
      const final = dropGuide(pos, size, overRuler(e.clientX, e.clientY));
      return { axis, index, pos: final ?? Math.round(pos), out: final === null };
    };
    setGuideDrag(at(ev));
    const move = (e: PointerEvent): void => setGuideDrag(at(e));
    const finish = (e: PointerEvent | null): void => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
      window.removeEventListener('pointercancel', cancel);
      window.removeEventListener('keydown', key, true);
      setGuideDrag(null);
      if (!e) return;
      const g = at(e);
      if (index === null) {
        if (!g.out) store.addGuide(axis, g.pos);
      } else if (g.out) store.removeGuide(axis, index);
      else store.moveGuide(axis, index, g.pos);
    };
    const up = (e: PointerEvent): void => finish(e);
    const cancel = (): void => finish(null);
    const key = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape') return;
      e.preventDefault();
      e.stopPropagation();
      finish(null);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
    window.addEventListener('pointercancel', cancel);
    window.addEventListener('keydown', key, true);
  };

  // ---- context menu -------------------------------------------------------------
  const editText = (id: string): void => {
    store.select([id]);
    setEditingText(id);
  };
  const slideMenuItems = () => canvasMenu(store, t, { background: () => setBgDialog(true) });

  /** Menu at a screen point: element menu on an element, otherwise the slide menu. */
  const openMenuAt = (clientX: number, clientY: number): void => {
    if (!slide) return;
    const p = toDesign(clientX, clientY);
    const e = hit(p.x, p.y, true);
    if (e) {
      if (!store.getSnapshot().selection.includes(e.id)) store.select([e.id]);
      setMenu({ x: clientX, y: clientY, items: elementMenu(store, t, { editText: e.type === 'text' && !e.locked ? () => editText(e.id) : undefined }) });
    } else {
      store.clearSelection();
      setMenu({ x: clientX, y: clientY, items: slideMenuItems() });
    }
  };

  /** Menu opened with the keyboard: for the selection, or for the slide. */
  const openKeyboardMenu = (): void => {
    const layer = layerRef.current;
    if (!slide || !layer) return;
    const r = layer.getBoundingClientRect();
    const sel = store.selected;
    if (sel.length) {
      const b = bounds(sel);
      const single = sel.length === 1 ? sel[0]! : null;
      setMenu({
        x: Math.round(r.left + ((b.x + b.r) / 2) * scale),
        y: Math.round(r.top + ((b.y + b.bt) / 2) * scale),
        items: elementMenu(store, t, { editText: single?.type === 'text' && !single.locked ? () => editText(single.id) : undefined }),
      });
    } else {
      setMenu({ x: Math.round(r.left + r.width / 2), y: Math.round(r.top + r.height / 2), items: slideMenuItems() });
    }
  };
  const keyboardMenu = useRef(openKeyboardMenu);
  keyboardMenu.current = openKeyboardMenu;

  // The context menu key with nothing focused targets the page body: open the canvas menu
  // there instead of the browser menu.
  useEffect(() => {
    const onCtx = (e: MouseEvent): void => {
      if (e.defaultPrevented || (e.target !== document.body && e.target !== document.documentElement)) return;
      e.preventDefault();
      keyboardMenu.current();
    };
    document.addEventListener('contextmenu', onCtx);
    return () => document.removeEventListener('contextmenu', onCtx);
  }, []);

  const onContextMenu = (ev: React.MouseEvent): void => {
    if (isEditable(ev.target)) return; // text editing keeps the browser's text menu (spelling)
    ev.preventDefault();
    if (drag || guideDrag) return;
    const native = ev.nativeEvent as MouseEvent & { pointerType?: string };
    const fromKeyboard = native.pointerType === '' || (ev.clientX === 0 && ev.clientY === 0);
    if (fromKeyboard) openKeyboardMenu();
    else openMenuAt(ev.clientX, ev.clientY);
  };

  // ---- pointer interaction --------------------------------------------------------
  const onPointerDown = (ev: React.PointerEvent): void => {
    if (ev.button !== 0 || !slide) return;
    const p = toDesign(ev.clientX, ev.clientY);
    const target = (ev.target as HTMLElement).dataset;
    if (target.handle && snap.selection.length === 1) {
      const id = snap.selection[0]!;
      const e = slide.elements.find((x) => x.id === id);
      if (!e || e.locked) return;
      store.begin();
      if (target.handle === 'rotate') setDrag({ kind: 'rotate', id, cx: e.x + e.w / 2, cy: e.y + e.h / 2 });
      else setDrag({ kind: 'resize', handle: target.handle, startX: p.x, startY: p.y, origin: { x: e.x, y: e.y, w: e.w, h: e.h }, id });
      (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
      ev.preventDefault();
      return;
    }
    const e = hit(p.x, p.y);
    if (!e) {
      if (!ev.shiftKey) store.clearSelection();
      setDrag({ kind: 'marquee', startX: p.x, startY: p.y, x: p.x, y: p.y });
      (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
      ev.preventDefault();
      return;
    }
    if (!snap.selection.includes(e.id) || ev.shiftKey) store.select([e.id], ev.shiftKey);
    const sel = store.selected.filter((x) => !x.locked);
    if (sel.length === 0) return;
    store.begin();
    setDrag({ kind: 'move', startX: p.x, startY: p.y, origin: new Map(sel.map((x) => [x.id, { x: x.x, y: x.y }])) });
    (ev.currentTarget as HTMLElement).setPointerCapture(ev.pointerId);
    ev.preventDefault();
  };

  const onPointerMove = (ev: React.PointerEvent): void => {
    if (!drag || !slide) return;
    const p = toDesign(ev.clientX, ev.clientY);
    const tol = SNAP_PX / scale;
    const grid = view.grid ? view.gridSize : 0;
    const snapGrid = (v: number): number => (grid && view.snap && !ev.altKey ? Math.round(v / grid) * grid : v);
    if (drag.kind === 'marquee') {
      setDrag({ ...drag, x: p.x, y: p.y });
      return;
    }
    if (drag.kind === 'move') {
      let dx = p.x - drag.startX;
      let dy = p.y - drag.startY;
      const ids = new Set(drag.origin.keys());
      const moved = slide.elements.filter((e) => ids.has(e.id));
      const g: SnapLine[] = [];
      if (view.snap && !ev.altKey && moved.length) {
        const minX = Math.min(...moved.map((e) => drag.origin.get(e.id)!.x)) + dx;
        const minY = Math.min(...moved.map((e) => drag.origin.get(e.id)!.y)) + dy;
        const maxX = Math.max(...moved.map((e) => drag.origin.get(e.id)!.x + e.w)) + dx;
        const maxY = Math.max(...moved.map((e) => drag.origin.get(e.id)!.y + e.h)) + dy;
        const cand = snapCandidates(slide, doc.width, doc.height, ids, snap.guides);
        const sx = nearest([minX, (minX + maxX) / 2, maxX], cand.xs, tol);
        const sy = nearest([minY, (minY + maxY) / 2, maxY], cand.ys, tol);
        if (sx) {
          dx += sx.delta;
          g.push({ axis: 'x', pos: sx.at });
        } else if (grid) dx = snapGrid(minX) - (minX - dx);
        if (sy) {
          dy += sy.delta;
          g.push({ axis: 'y', pos: sy.at });
        } else if (grid) dy = snapGrid(minY) - (minY - dy);
      }
      setSnapLines(g);
      store.preview((d) => {
        const s = d.slides.find((x) => x.id === slide.id);
        for (const e of s?.elements ?? []) {
          const o = drag.origin.get(e.id);
          if (o) {
            e.x = Math.round(o.x + dx);
            e.y = Math.round(o.y + dy);
          }
        }
      });
      return;
    }
    if (drag.kind === 'resize') {
      const o = drag.origin;
      let { x, y, w, h } = o;
      const dx = p.x - drag.startX;
      const dy = p.y - drag.startY;
      const hd = drag.handle;
      if (hd.includes('e')) w = o.w + dx;
      if (hd.includes('s')) h = o.h + dy;
      if (hd.includes('w')) {
        w = o.w - dx;
        x = o.x + dx;
      }
      if (hd.includes('n')) {
        h = o.h - dy;
        y = o.y + dy;
      }
      const keepRatio = ev.shiftKey && hd.length === 2;
      if (keepRatio) {
        // Keep aspect ratio on corner handles.
        const ratio = o.w / o.h;
        if (w / h > ratio) w = h * ratio;
        else h = w / ratio;
        if (hd.includes('w')) x = o.x + o.w - w;
        if (hd.includes('n')) y = o.y + o.h - h;
      }
      // The dragged edges snap to guides, slide edges/center and other elements, else to
      // the grid (the opposite edge stays where it is).
      const lines: SnapLine[] = [];
      if (view.snap && !ev.altKey && !keepRatio) {
        const cand = snapCandidates(slide, doc.width, doc.height, new Set([drag.id]), snap.guides);
        const edge = (value: number, targets: number[], axis: GuideAxis): number | null => {
          const s = nearest([value], targets, tol);
          if (!s) return null;
          lines.push({ axis, pos: s.at });
          return s.delta;
        };
        const ex = hd.includes('e') ? edge(x + w, cand.xs, 'x') : hd.includes('w') ? edge(x, cand.xs, 'x') : null;
        const ey = hd.includes('s') ? edge(y + h, cand.ys, 'y') : hd.includes('n') ? edge(y, cand.ys, 'y') : null;
        if (ex !== null) {
          if (hd.includes('w')) x += ex;
          w += hd.includes('w') ? -ex : ex;
        } else if (hd.includes('e') || hd.includes('w')) {
          w = snapGrid(w);
          if (hd.includes('w')) x = o.x + o.w - w;
        }
        if (ey !== null) {
          if (hd.includes('n')) y += ey;
          h += hd.includes('n') ? -ey : ey;
        } else if (hd.includes('s') || hd.includes('n')) {
          h = snapGrid(h);
          if (hd.includes('n')) y = o.y + o.h - h;
        }
      }
      setSnapLines(lines);
      w = Math.max(4, w);
      h = Math.max(4, h);
      store.preview((d) => {
        const e = d.slides.find((s) => s.id === slide.id)?.elements.find((x) => x.id === drag.id);
        if (e) Object.assign(e, { x: Math.round(x), y: Math.round(y), w: Math.round(w), h: Math.round(h) });
      });
      return;
    }
    if (drag.kind === 'rotate') {
      let deg = (Math.atan2(p.y - drag.cy, p.x - drag.cx) * 180) / Math.PI + 90;
      if (ev.shiftKey) deg = Math.round(deg / 15) * 15;
      deg = ((deg + 540) % 360) - 180;
      store.preview((d) => {
        const e = d.slides.find((s) => s.id === slide.id)?.elements.find((x) => x.id === drag.id);
        if (e) e.rotation = Math.round(deg);
      });
    }
  };

  const onPointerUp = (): void => {
    if (!drag || !slide) return;
    if (drag.kind === 'marquee') {
      const x1 = Math.min(drag.startX, drag.x);
      const y1 = Math.min(drag.startY, drag.y);
      const x2 = Math.max(drag.startX, drag.x);
      const y2 = Math.max(drag.startY, drag.y);
      if (x2 - x1 > 2 || y2 - y1 > 2) {
        const ids = slide.elements.filter((e) => !e.locked && !e.hidden && e.x >= x1 && e.y >= y1 && e.x + e.w <= x2 && e.y + e.h <= y2).map((e) => e.id);
        store.select(ids, true);
      }
    } else {
      store.commit();
    }
    setDrag(null);
    setSnapLines([]);
  };

  const onDoubleClick = (ev: React.MouseEvent): void => {
    const p = toDesign(ev.clientX, ev.clientY);
    const e = hit(p.x, p.y);
    if (e?.type === 'text') editText(e.id);
  };

  const rulers = view.rulers;
  const stageClass = `editor-stage ${rulers ? 'with-rulers' : ''} ${guideDrag ? `dragging-guide-${guideDrag.axis}` : ''}`;
  if (!slide) {
    return (
      <div className={stageClass}>
        <div className="editor-canvas-outer" ref={outerRef} />
      </div>
    );
  }
  const selected = slide.elements.filter((e) => snap.selection.includes(e.id));
  const selBounds = selected.length ? bounds(selected) : null;
  const textEl = editingText ? slide.elements.find((e) => e.id === editingText && e.type === 'text') : undefined;
  const guideLine = (axis: GuideAxis, pos: number, index: number | null, extra = ''): React.ReactNode => (
    <div
      key={`${axis}-${index ?? 'new'}`}
      className={`user-guide user-guide-${axis} ${extra}`}
      style={axis === 'x' ? { left: pos * scale } : { top: pos * scale }}
      onPointerDown={index === null ? undefined : (ev) => ev.button === 0 && startGuideDrag(axis, index, ev)}
      onContextMenu={
        index === null
          ? undefined
          : (ev) => {
              ev.preventDefault();
              ev.stopPropagation();
              setMenu({ x: ev.clientX, y: ev.clientY, items: guideMenu(store, t, axis, index) });
            }
      }
    />
  );
  return (
    <div className={stageClass}>
      {rulers && (
        <>
          <div className="ruler-corner" />
          <Ruler
            axis="x"
            rulerRef={rulerXRef}
            outerRef={outerRef}
            slideRef={slideRef}
            scale={scale}
            size={doc.width}
            range={selBounds ? [selBounds.x, selBounds.r] : null}
            marks={snap.guides.x}
            onGuideStart={(ev) => startGuideDrag('y', null, ev)}
          />
          <Ruler
            axis="y"
            rulerRef={rulerYRef}
            outerRef={outerRef}
            slideRef={slideRef}
            scale={scale}
            size={doc.height}
            range={selBounds ? [selBounds.y, selBounds.bt] : null}
            marks={snap.guides.y}
            onGuideStart={(ev) => startGuideDrag('x', null, ev)}
          />
        </>
      )}
      <div
        className="editor-canvas-outer"
        ref={outerRef}
        tabIndex={0}
        aria-label={t('editor.canvas')}
        // A click beside the slide selects nothing: the panel shows the presentation.
        onPointerDown={(e) => e.target === e.currentTarget && e.button === 0 && store.focusPresentation()}
        onContextMenu={onContextMenu}
      >
        <div
          ref={slideRef}
          className={`editor-canvas ${view.grid ? 'with-grid' : ''}`}
          style={{ width: doc.width * scale, height: doc.height * scale, ['--s' as string]: String(scale), ['--grid' as string]: `${view.gridSize * scale}px` }}
        >
          <div className="os-bg" ref={bgRef} />
          <div className="editor-layer" ref={layerRef} />
          <div className="editor-overlay" onPointerDown={onPointerDown} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerCancel={onPointerUp} onDoubleClick={onDoubleClick}>
            {snap.guides.x.map((pos, i) => (guideDrag?.axis === 'x' && guideDrag.index === i ? null : guideLine('x', pos, i)))}
            {snap.guides.y.map((pos, i) => (guideDrag?.axis === 'y' && guideDrag.index === i ? null : guideLine('y', pos, i)))}
            {guideDrag && !guideDrag.out && (
              <>
                {guideLine(guideDrag.axis, guideDrag.pos, null, 'dragging')}
                <div className={`guide-label guide-label-${guideDrag.axis}`} style={guideDrag.axis === 'x' ? { left: guideDrag.pos * scale } : { top: guideDrag.pos * scale }}>
                  {guideDrag.pos}
                </div>
              </>
            )}
            {selected.map((e) => (
              <div
                key={e.id}
                className={`sel-box ${e.locked ? 'locked' : ''}`}
                style={{ left: e.x * scale, top: e.y * scale, width: e.w * scale, height: e.h * scale, transform: e.rotation ? `rotate(${e.rotation}deg)` : undefined }}
              >
                {selected.length === 1 && !e.locked && !editingText && (
                  <>
                    {HANDLES.map((h) => (
                      <div key={h} className={`handle handle-${h}`} data-handle={h} />
                    ))}
                    <div className="handle handle-rotate" data-handle="rotate" />
                  </>
                )}
              </div>
            ))}
            {snapLines.map((g, i) => (
              <div key={i} className={`guide guide-${g.axis}`} style={g.axis === 'x' ? { left: g.pos * scale } : { top: g.pos * scale }} />
            ))}
            {drag?.kind === 'marquee' && (
              <div
                className="marquee"
                style={{
                  left: Math.min(drag.startX, drag.x) * scale,
                  top: Math.min(drag.startY, drag.y) * scale,
                  width: Math.abs(drag.x - drag.startX) * scale,
                  height: Math.abs(drag.y - drag.startY) * scale,
                }}
              />
            )}
          </div>
          {textEl && textEl.type === 'text' && (
            <TextEditor
              element={textEl}
              scale={scale}
              onChange={(docText) =>
                store.updateElements([textEl.id], (x) => {
                  if (x.type === 'text') x.doc = docText;
                })
              }
              onDone={() => setEditingText(null)}
              store={store}
            />
          )}
        </div>
      </div>
      {menu && <ContextMenu menu={menu} onClose={() => setMenu(null)} label={t('editor.menu.label')} />}
      {bgDialog && <SlideBackgroundDialog store={store} slideId={slide.id} onClose={() => setBgDialog(false)} />}
    </div>
  );
}
