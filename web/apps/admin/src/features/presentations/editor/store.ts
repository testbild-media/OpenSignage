// Editor state: document, selection, undo/redo, clipboard. Framework free (tested with
// vitest); React reads it through useSyncExternalStore.
import type { Presentation, Slide, SlideElement } from '@opensignage/contracts';
import { loadGuides, saveGuides, type GuideAxis, type Guides } from './guides';

export interface ViewState {
  zoom: number | 'fit';
  grid: boolean;
  gridSize: number;
  snap: boolean;
  rulers: boolean;
}

/**
 * What the properties panel is about when no element is selected: the whole presentation
 * (nothing selected – a click beside the slide) or the current slide (a click on it or on
 * its thumbnail). A selected element always wins.
 */
export type Focus = 'presentation' | 'slide';

export interface EditorSnapshot {
  doc: Presentation;
  slideId: string;
  selection: string[];
  focus: Focus;
  dirty: boolean;
  canUndo: boolean;
  canRedo: boolean;
  view: ViewState;
  guides: Guides;
  version: number;
}

/** Per-slide media limits of the player: one video and one audio element. */
export type MediaLimit = 'video' | 'audio';

/** The limit that adding `added` to a slide with `existing` elements would break. */
export function mediaLimit(existing: readonly { type: string }[], added: readonly { type: string }[]): MediaLimit | null {
  for (const kind of ['video', 'audio'] as const) {
    if (!added.some((e) => e.type === kind)) continue;
    const n = existing.filter((e) => e.type === kind).length + added.filter((e) => e.type === kind).length;
    if (n > 1) return kind;
  }
  return null;
}

const MAX_HISTORY = 200;
const CLIPBOARD_KEY = 'os.clipboard';

export function newId(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return 'id-' + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

function clone<T>(v: T): T {
  return structuredClone(v);
}

export class EditorStore {
  private doc: Presentation;
  private slideId: string;
  private selection: string[] = [];
  private focus: Focus = 'presentation';
  private undoStack: Presentation[] = [];
  private redoStack: Presentation[] = [];
  private pending: Presentation | null = null; // doc before a continuous interaction
  private listeners = new Set<() => void>();
  private snapshot: EditorSnapshot;
  private version = 0;
  dirty = false;
  view: ViewState = { zoom: 'fit', grid: false, gridSize: 20, snap: true, rulers: true };
  guides: Guides;
  /** Called when a change is refused because of a media limit (the UI shows why). */
  onLimit: ((kind: MediaLimit) => void) | null = null;
  private readonly persistId: string;

  /** persistId keys the editor-only state kept in this browser (guides). */
  constructor(doc: Presentation, persistId: string = doc.id) {
    this.doc = doc;
    this.persistId = persistId;
    this.slideId = doc.slides[0]?.id ?? '';
    this.guides = loadGuides(persistId);
    this.snapshot = this.makeSnapshot();
  }

  // ---- subscription ---------------------------------------------------------
  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };
  getSnapshot = (): EditorSnapshot => this.snapshot;

  private makeSnapshot(): EditorSnapshot {
    return {
      doc: this.doc,
      slideId: this.slideId,
      selection: this.selection,
      focus: this.focus,
      dirty: this.dirty,
      canUndo: this.undoStack.length > 0,
      canRedo: this.redoStack.length > 0,
      view: this.view,
      guides: this.guides,
      version: this.version,
    };
  }

  private emit(): void {
    this.version++;
    this.snapshot = this.makeSnapshot();
    for (const l of this.listeners) l();
  }

  // ---- accessors ------------------------------------------------------------
  get presentation(): Presentation {
    return this.doc;
  }
  get slide(): Slide | undefined {
    return this.doc.slides.find((s) => s.id === this.slideId);
  }
  get selected(): SlideElement[] {
    const s = this.slide;
    if (!s) return [];
    return s.elements.filter((e) => this.selection.includes(e.id));
  }

  // ---- document mutations -----------------------------------------------------
  /** One undoable change. mutate works on a deep clone of the document. */
  apply(mutate: (doc: Presentation) => void): void {
    const before = this.pending ?? this.doc;
    const next = clone(this.pending ? this.doc : this.doc);
    mutate(next);
    this.pending = null;
    this.undoStack.push(before);
    if (this.undoStack.length > MAX_HISTORY) this.undoStack.shift();
    this.redoStack = [];
    this.doc = next;
    this.fixSelection();
    this.dirty = true;
    this.emit();
  }

  /** Starts a continuous interaction (drag, resize, slider). */
  begin(): void {
    if (!this.pending) this.pending = this.doc;
  }

  /** Live preview during an interaction (no history entry). */
  preview(mutate: (doc: Presentation) => void): void {
    if (!this.pending) this.begin();
    const next = clone(this.doc);
    mutate(next);
    this.doc = next;
    this.emit();
  }

  /** Ends an interaction as one undo step (no-op if nothing changed). */
  commit(): void {
    if (!this.pending) return;
    const before = this.pending;
    this.pending = null;
    if (JSON.stringify(before) === JSON.stringify(this.doc)) return;
    this.undoStack.push(before);
    if (this.undoStack.length > MAX_HISTORY) this.undoStack.shift();
    this.redoStack = [];
    this.dirty = true;
    this.emit();
  }

  cancel(): void {
    if (!this.pending) return;
    this.doc = this.pending;
    this.pending = null;
    this.emit();
  }

  undo(): void {
    const prev = this.undoStack.pop();
    if (!prev) return;
    this.redoStack.push(this.doc);
    this.doc = prev;
    this.fixSelection();
    this.dirty = true;
    this.emit();
  }

  redo(): void {
    const next = this.redoStack.pop();
    if (!next) return;
    this.undoStack.push(this.doc);
    this.doc = next;
    this.fixSelection();
    this.dirty = true;
    this.emit();
  }

  /** Replaces the whole document (server reload); clears history. */
  reset(doc: Presentation): void {
    this.doc = doc;
    this.undoStack = [];
    this.redoStack = [];
    this.pending = null;
    if (!doc.slides.some((s) => s.id === this.slideId)) this.slideId = doc.slides[0]?.id ?? '';
    this.fixSelection();
    this.dirty = false;
    this.emit();
  }

  markSaved(savedDoc: Presentation): void {
    if (savedDoc === this.doc) this.dirty = false;
    this.emit();
  }

  setView(patch: Partial<ViewState>): void {
    this.view = { ...this.view, ...patch };
    this.emit();
  }

  // ---- guides (editor aid, not part of the document, no undo) -----------------
  setGuides(next: Guides): void {
    this.guides = next;
    saveGuides(this.persistId, next);
    this.emit();
  }

  addGuide(axis: GuideAxis, pos: number): void {
    this.setGuides({ ...this.guides, [axis]: [...this.guides[axis], pos] });
  }

  moveGuide(axis: GuideAxis, index: number, pos: number): void {
    this.setGuides({ ...this.guides, [axis]: this.guides[axis].map((p, i) => (i === index ? pos : p)) });
  }

  removeGuide(axis: GuideAxis, index: number): void {
    this.setGuides({ ...this.guides, [axis]: this.guides[axis].filter((_, i) => i !== index) });
  }

  clearGuides(): void {
    this.setGuides({ x: [], y: [] });
  }

  // ---- media limits ---------------------------------------------------------
  /**
   * True if `added` may be put on the slide; otherwise reports the limit through onLimit
   * (the same message as on insert) and returns false.
   */
  checkLimit(added: readonly { type: string }[], slideId = this.slideId): boolean {
    const s = this.doc.slides.find((x) => x.id === slideId);
    const kind = mediaLimit(s?.elements ?? [], added);
    if (!kind) return true;
    this.onLimit?.(kind);
    return false;
  }

  // ---- selection ------------------------------------------------------------
  selectSlide(id: string): void {
    if (this.slideId === id && this.focus === 'slide' && this.selection.length === 0) return;
    this.slideId = id;
    this.selection = [];
    this.focus = 'slide';
    this.emit();
  }

  /** The current slide itself (its properties), no element. */
  focusSlide(): void {
    this.selectSlide(this.slideId);
  }

  /** Nothing selected: the panel shows the presentation. */
  focusPresentation(): void {
    if (this.focus === 'presentation' && this.selection.length === 0) return;
    this.selection = [];
    this.focus = 'presentation';
    this.emit();
  }

  /** Esc: elements → their slide → the presentation. */
  escape(): void {
    if (this.selection.length) this.clearSelection();
    else this.focusPresentation();
  }

  /** Selects elements; group members are always selected together. */
  select(ids: string[], additive = false): void {
    const s = this.slide;
    if (!s) return;
    const expanded = new Set(additive ? this.selection : []);
    for (const id of ids) {
      const el = s.elements.find((e) => e.id === id);
      if (!el) continue;
      if (additive && this.selection.includes(id) && ids.length === 1) {
        const group = el.groupId ? s.elements.filter((e) => e.groupId === el.groupId).map((e) => e.id) : [id];
        for (const g of group) expanded.delete(g);
        continue;
      }
      if (el.groupId) for (const e of s.elements) if (e.groupId === el.groupId) expanded.add(e.id);
      expanded.add(id);
    }
    this.selection = s.elements.map((e) => e.id).filter((id) => expanded.has(id));
    this.focus = 'slide';
    this.emit();
  }

  selectAll(): void {
    this.selection = this.slide?.elements.filter((e) => !e.locked && !e.hidden).map((e) => e.id) ?? [];
    this.focus = 'slide';
    this.emit();
  }

  /** No element selected; the panel shows the slide they are on. */
  clearSelection(): void {
    if (this.selection.length === 0 && this.focus === 'slide') return;
    this.selection = [];
    this.focus = 'slide';
    this.emit();
  }

  private fixSelection(): void {
    if (!this.doc.slides.some((s) => s.id === this.slideId)) this.slideId = this.doc.slides[0]?.id ?? '';
    const s = this.slide;
    this.selection = s ? this.selection.filter((id) => s.elements.some((e) => e.id === id)) : [];
  }

  // ---- element helpers ------------------------------------------------------
  /** Updates elements of the current slide (one undo step). */
  updateElements(ids: string[], fn: (e: SlideElement) => void): void {
    const slideId = this.slideId;
    this.apply((doc) => {
      const s = doc.slides.find((x) => x.id === slideId);
      if (!s) return;
      for (const e of s.elements) if (ids.includes(e.id)) fn(e);
    });
  }

  addElement(el: SlideElement): boolean {
    if (!this.checkLimit([el])) return false;
    const slideId = this.slideId;
    this.apply((doc) => {
      doc.slides.find((x) => x.id === slideId)?.elements.push(el);
    });
    this.selection = [el.id];
    this.emit();
    return true;
  }

  deleteSelection(): void {
    const ids = this.selected.filter((e) => !e.locked).map((e) => e.id);
    if (ids.length === 0) return;
    const slideId = this.slideId;
    this.apply((doc) => {
      const s = doc.slides.find((x) => x.id === slideId);
      if (s) s.elements = s.elements.filter((e) => !ids.includes(e.id));
    });
  }

  /** Moves selected elements one step / to the end of the z-order. */
  arrange(mode: 'front' | 'back' | 'forward' | 'backward'): void {
    const ids = new Set(this.selection);
    if (ids.size === 0) return;
    const slideId = this.slideId;
    this.apply((doc) => {
      const s = doc.slides.find((x) => x.id === slideId);
      if (!s) return;
      const sel = s.elements.filter((e) => ids.has(e.id));
      const rest = s.elements.filter((e) => !ids.has(e.id));
      if (mode === 'front') s.elements = [...rest, ...sel];
      else if (mode === 'back') s.elements = [...sel, ...rest];
      else {
        const arr = [...s.elements];
        const order = mode === 'forward' ? [...arr.keys()].reverse() : [...arr.keys()];
        for (const i of order) {
          const j = mode === 'forward' ? i + 1 : i - 1;
          if (j < 0 || j >= arr.length) continue;
          if (ids.has(arr[i]!.id) && !ids.has(arr[j]!.id)) [arr[i], arr[j]] = [arr[j]!, arr[i]!];
        }
        s.elements = arr;
      }
    });
  }

  /** Moves an element to a new index in the z-order (layers drag & drop). */
  moveElement(id: string, toIndex: number): void {
    const slideId = this.slideId;
    this.apply((doc) => {
      const s = doc.slides.find((x) => x.id === slideId);
      if (!s) return;
      const from = s.elements.findIndex((e) => e.id === id);
      if (from < 0) return;
      const [el] = s.elements.splice(from, 1);
      s.elements.splice(Math.max(0, Math.min(s.elements.length, toIndex)), 0, el!);
    });
  }

  align(mode: 'left' | 'center' | 'right' | 'top' | 'middle' | 'bottom'): void {
    const sel = this.selected.filter((e) => !e.locked);
    if (sel.length === 0) return;
    // One element aligns to the slide, several align to their common bounds.
    const b = sel.length === 1 ? { x: 0, y: 0, r: this.doc.width, bt: this.doc.height } : bounds(sel);
    this.updateElements(
      sel.map((e) => e.id),
      (e) => {
        if (mode === 'left') e.x = b.x;
        if (mode === 'right') e.x = b.r - e.w;
        if (mode === 'center') e.x = (b.x + b.r) / 2 - e.w / 2;
        if (mode === 'top') e.y = b.y;
        if (mode === 'bottom') e.y = b.bt - e.h;
        if (mode === 'middle') e.y = (b.y + b.bt) / 2 - e.h / 2;
      },
    );
  }

  distribute(axis: 'h' | 'v'): void {
    const sel = this.selected.filter((e) => !e.locked);
    if (sel.length < 3) return;
    const sorted = [...sel].sort((a, b) => (axis === 'h' ? a.x - b.x : a.y - b.y));
    const first = sorted[0]!;
    const last = sorted[sorted.length - 1]!;
    const total = sorted.reduce((s, e) => s + (axis === 'h' ? e.w : e.h), 0);
    const span = axis === 'h' ? last.x + last.w - first.x : last.y + last.h - first.y;
    const gap = (span - total) / (sorted.length - 1);
    const pos = new Map<string, number>();
    let cur = axis === 'h' ? first.x : first.y;
    for (const e of sorted) {
      pos.set(e.id, cur);
      cur += (axis === 'h' ? e.w : e.h) + gap;
    }
    this.updateElements([...pos.keys()], (e) => {
      if (axis === 'h') e.x = pos.get(e.id)!;
      else e.y = pos.get(e.id)!;
    });
  }

  group(): void {
    const sel = this.selected;
    if (sel.length < 2) return;
    const gid = newId();
    this.updateElements(
      sel.map((e) => e.id),
      (e) => {
        e.groupId = gid;
      },
    );
  }

  ungroup(): void {
    const sel = this.selected.filter((e) => e.groupId);
    if (sel.length === 0) return;
    this.updateElements(
      sel.map((e) => e.id),
      (e) => {
        delete e.groupId;
      },
    );
  }

  // ---- clipboard ------------------------------------------------------------
  copy(): void {
    const els = clone(this.selected);
    if (els.length === 0) return;
    try {
      localStorage.setItem(CLIPBOARD_KEY, JSON.stringify({ kind: 'os-elements', elements: els }));
    } catch {
      // storage unavailable: clipboard only works within this tab
    }
    memoryClipboard = els;
  }

  cut(): void {
    this.copy();
    this.deleteSelection();
  }

  /** True if paste has something to insert (this tab, another tab or presentation). */
  canPaste(): boolean {
    return readClipboard() !== null;
  }

  paste(): void {
    const els = readClipboard();
    if (els) this.insertCopies(els, 20);
  }

  duplicate(): void {
    const els = clone(this.selected);
    if (els.length) this.insertCopies(els, 20);
  }

  private insertCopies(src: SlideElement[], offset: number): void {
    if (!this.checkLimit(src)) return;
    const groupMap = new Map<string, string>();
    const copies = src.map((e) => {
      const c = clone(e);
      c.id = newId();
      c.x += offset;
      c.y += offset;
      c.locked = false;
      if (c.groupId) {
        if (!groupMap.has(c.groupId)) groupMap.set(c.groupId, newId());
        c.groupId = groupMap.get(c.groupId);
      }
      return c;
    });
    const slideId = this.slideId;
    this.apply((doc) => {
      doc.slides.find((x) => x.id === slideId)?.elements.push(...copies);
    });
    this.selection = copies.map((c) => c.id);
    this.emit();
  }

  /** Copies the selected (pinned) elements onto all other slides at the same place. */
  copyToAllSlides(): void {
    const els = clone(this.selected);
    if (els.length === 0) return;
    const current = this.slideId;
    for (const s of this.doc.slides) if (s.id !== current && !this.checkLimit(els, s.id)) return;
    this.apply((doc) => {
      for (const s of doc.slides) {
        if (s.id === current) continue;
        for (const e of els) {
          const c = clone(e);
          c.id = newId();
          s.elements.push(c);
        }
      }
    });
  }

  // ---- slides ---------------------------------------------------------------
  addSlide(slide: Slide, afterId?: string): void {
    this.apply((doc) => {
      const i = afterId ? doc.slides.findIndex((s) => s.id === afterId) : doc.slides.length - 1;
      doc.slides.splice(i + 1, 0, slide);
    });
    this.selectSlide(slide.id);
  }

  duplicateSlide(id: string): void {
    const s = this.doc.slides.find((x) => x.id === id);
    if (!s) return;
    const c = clone(s);
    c.id = newId();
    c.name = s.name;
    c.elements = c.elements.map((e) => ({ ...e, id: newId() }));
    // A slide that already breaks a limit (older draft) is not multiplied.
    const over = mediaLimit([], c.elements);
    if (over) {
      this.onLimit?.(over);
      return;
    }
    this.addSlide(c, id);
  }

  deleteSlide(id: string): void {
    if (this.doc.slides.length <= 1) return;
    const idx = this.doc.slides.findIndex((s) => s.id === id);
    this.apply((doc) => {
      doc.slides = doc.slides.filter((s) => s.id !== id);
    });
    const next = this.doc.slides[Math.max(0, idx - 1)];
    if (next) this.selectSlide(next.id);
  }

  moveSlide(id: string, toIndex: number): void {
    this.apply((doc) => {
      const from = doc.slides.findIndex((s) => s.id === id);
      if (from < 0) return;
      const [s] = doc.slides.splice(from, 1);
      doc.slides.splice(Math.max(0, Math.min(doc.slides.length, toIndex)), 0, s!);
    });
  }

  updateSlide(id: string, fn: (s: Slide) => void): void {
    this.apply((doc) => {
      const s = doc.slides.find((x) => x.id === id);
      if (s) fn(s);
    });
  }
}

let memoryClipboard: SlideElement[] | null = null;

function readClipboard(): SlideElement[] | null {
  let els: SlideElement[] | null = null;
  try {
    const raw = localStorage.getItem(CLIPBOARD_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as { kind?: string; elements?: SlideElement[] };
      if (parsed.kind === 'os-elements' && Array.isArray(parsed.elements)) els = parsed.elements;
    }
  } catch {
    els = null;
  }
  els ??= memoryClipboard;
  return els && els.length > 0 ? els : null;
}

export function bounds(els: { x: number; y: number; w: number; h: number }[]): { x: number; y: number; r: number; bt: number } {
  return {
    x: Math.min(...els.map((e) => e.x)),
    y: Math.min(...els.map((e) => e.y)),
    r: Math.max(...els.map((e) => e.x + e.w)),
    bt: Math.max(...els.map((e) => e.y + e.h)),
  };
}

/** Scales a presentation to a new design size (positions per axis, sizes uniformly). */
export function resizePresentation(doc: Presentation, width: number, height: number): void {
  const sx = width / doc.width;
  const sy = height / doc.height;
  const s = Math.min(sx, sy);
  const scaleStyle = (st: { size: number; letterSpacing: number; outline?: { width: number }; shadow?: { dx: number; dy: number } }): void => {
    st.size *= s;
    st.letterSpacing *= s;
    if (st.outline) st.outline.width *= s;
    if (st.shadow) {
      st.shadow.dx *= s;
      st.shadow.dy *= s;
    }
  };
  // Copy first: styles may share references (scaling twice would be wrong).
  const ty = doc.typography;
  for (const k of ['heading', 'subheading', 'item', 'description', 'price'] as const) {
    ty[k] = structuredClone(ty[k]);
    scaleStyle(ty[k]);
  }
  for (const slide of doc.slides) {
    for (const e of slide.elements) {
      const cx = (e.x + e.w / 2) * sx;
      const cy = (e.y + e.h / 2) * sy;
      e.w *= s;
      e.h *= s;
      e.x = cx - e.w / 2;
      e.y = cy - e.h / 2;
      if ('style' in e && e.style) scaleStyle(e.style);
      if (e.type === 'text') {
        if (e.padding) e.padding = e.padding.map((p) => p * s) as [number, number, number, number];
        for (const p of e.doc.paragraphs) for (const r of p.runs) if (r.marks?.size) r.marks.size *= s;
      }
      if (e.type === 'shape') {
        e.stroke.width *= s;
        if (e.radius) e.radius *= s;
      }
      if (e.type === 'productList') {
        if (e.rowGap) e.rowGap *= s;
        if (e.columnGap) e.columnGap *= s;
      }
      if ((e.type === 'productList' || e.type === 'product') && e.overrides) {
        // Overrides are absolute sizes like the typography they replace.
        for (const m of Object.values(e.overrides)) {
          if (m?.size) m.size *= s;
          if (m?.letterSpacing) m.letterSpacing *= s;
        }
      }
    }
  }
  doc.width = width;
  doc.height = height;
}
