import type { Background, ScaleMode, Slide, SlideElement, Transition } from '@opensignage/contracts';
import type { ElementHandle, RenderEnv } from './env';
import { createVideo, objectFit, releaseVideo, renderElement } from './elements';
import { injectRenderCss } from './css';
import { color, round } from './units';
import { watchVideo, type VideoWatch } from './video';

/** A slide to show, with its design size (presentations may differ in size). */
export interface StageSlide {
  slide: Slide;
  width: number;
  height: number;
  env: RenderEnv;
  /** Unique per showing; used to restart identical consecutive slides cleanly. */
  key: string;
  /** Synchronous playback: a newly started background video begins this far in (ms). */
  startAtMs?: number;
}

interface BgNode {
  sig: string;
  el: HTMLElement;
  video?: HTMLVideoElement;
  watch?: VideoWatch;
  /** The watch reported the end (possibly a stall just before it, see video.ts). */
  ended?: boolean;
}

interface ShownContent {
  el: HTMLElement;
  handles: ElementHandle[];
}

interface PinnedNode {
  sig: string;
  handle: ElementHandle;
}

export interface StageEvents {
  /** The background video of the current slide ended (for "until video ends"). */
  onBackgroundEnded?: () => void;
  onMediaFailed?: (reason: string) => void;
}

const SLIDE_OFFSET = 1.0; // slide transitions move by one canvas width/height

/** Signature of a background: identical signatures continue without a transition. */
export function backgroundSignature(b: Background): string {
  switch (b.type) {
    case 'color':
      return `c:${b.color}`;
    case 'gradient':
      return `g:${b.color}:${b.angle}:${b.stops.map((s) => s.color + '@' + s.at).join(',')}`;
    case 'image':
      return `i:${b.assetId}:${b.fit}:${b.color}`;
    case 'video':
      return `v:${b.assetId}:${b.fit}:${b.color}`;
  }
}

/** Signature of a pinned element: identical content and placement stays in place. */
export function elementSignature(e: SlideElement): string {
  const { id: _id, name: _name, locked: _locked, groupId: _g, ...rest } = e;
  return JSON.stringify(rest);
}

/**
 * The stage owns the screen area: it fits the design canvas into the host according to
 * the scale mode and performs transitions using three layers (background, content,
 * pinned). Only transform and opacity are animated.
 */
export class Stage {
  readonly root: HTMLElement;
  private canvas: HTMLElement;
  private bgLayer: HTMLElement;
  private contentLayer: HTMLElement;
  private pinnedLayer: HTMLElement;
  private bg: BgNode | null = null;
  private content: ShownContent | null = null;
  private pinned: PinnedNode[] = [];
  private design = { w: 1920, h: 1080 };
  private scaleMode: ScaleMode = 'contain';
  private resizeObs: ResizeObserver;
  private generation = 0;
  private shiftTimer = 0;
  events: StageEvents = {};

  constructor(host: HTMLElement, scaleMode: ScaleMode = 'contain') {
    injectRenderCss(host.ownerDocument);
    this.scaleMode = scaleMode;
    this.root = document.createElement('div');
    this.root.className = 'os-stage';
    this.canvas = this.newCanvas();
    this.bgLayer = this.canvas.children[0] as HTMLElement;
    this.contentLayer = this.canvas.children[1] as HTMLElement;
    this.pinnedLayer = this.canvas.children[2] as HTMLElement;
    this.root.appendChild(this.canvas);
    host.appendChild(this.root);
    this.resizeObs = new ResizeObserver(() => this.fit());
    this.resizeObs.observe(this.root);
  }

  private newCanvas(): HTMLElement {
    const c = document.createElement('div');
    c.className = 'os-canvas';
    for (const cls of ['os-layer os-bgl', 'os-layer os-contentl', 'os-layer os-pinnedl']) {
      const l = document.createElement('div');
      l.className = cls;
      c.appendChild(l);
    }
    return c;
  }

  setScaleMode(mode: ScaleMode): void {
    this.scaleMode = mode;
    this.fit();
  }

  /** Enables a slow plasma-protection pixel shift (a few pixels, every 3 minutes). */
  setPlasmaShift(on: boolean): void {
    clearInterval(this.shiftTimer);
    this.root.style.transform = '';
    if (!on) return;
    let i = 0;
    const offsets = [[0, 0], [2, 1], [1, 3], [-1, 2], [-2, -1], [1, -2]];
    this.shiftTimer = window.setInterval(() => {
      i = (i + 1) % offsets.length;
      const [x, y] = offsets[i]!;
      this.root.style.transform = `translate(${x}px, ${y}px)`;
    }, 180_000);
  }

  /** Current CSS scale factor (design px → CSS px). */
  scale(): number {
    const r = this.root.getBoundingClientRect();
    const vw = r.width || window.innerWidth;
    const vh = r.height || window.innerHeight;
    const { w, h } = this.design;
    switch (this.scaleMode) {
      case 'none':
        return 1 / (window.devicePixelRatio || 1);
      case 'cover':
        return Math.max(vw / w, vh / h);
      default:
        return Math.min(vw / w, vh / h);
    }
  }

  private fit(canvas: HTMLElement = this.canvas): void {
    const s = this.scale();
    const { w, h } = this.design;
    const st = canvas.style;
    st.setProperty('--s', String(round(s * 10000) / 10000));
    st.width = `${round(w * s)}px`;
    st.height = `${round(h * s)}px`;
    st.marginLeft = `${round((-w * s) / 2)}px`;
    st.marginTop = `${round((-h * s) / 2)}px`;
  }

  /** Shows a slide with its transition. Resolves when the transition finished. */
  async show(next: StageSlide): Promise<void> {
    const gen = ++this.generation;
    const t = next.slide.transition;
    this.root.style.backgroundColor = color(next.slide.background.color, '#000');
    if (next.width !== this.design.w || next.height !== this.design.h || !this.content) {
      if (this.content && (next.width !== this.design.w || next.height !== this.design.h)) {
        await this.swapCanvas(next, t, gen);
        return;
      }
      this.design = { w: next.width, h: next.height };
      this.fit();
    }
    // Background: identical signature → keep (video continues), else transition.
    const sig = backgroundSignature(next.slide.background);
    const anims: Promise<void>[] = [];
    if (!this.bg || this.bg.sig !== sig) {
      const nb = this.buildBackground(next);
      const old = this.bg;
      this.bg = nb;
      this.bgLayer.appendChild(nb.el);
      nb.watch?.start();
      if (old) {
        // The outgoing video stops decoding right away: at most one active background decoder.
        old.watch?.stop();
        old.video?.pause();
        anims.push(this.transition(old.el, nb.el, t).then(() => this.dropBackground(old)));
      }
    } else if (this.bg.video) {
      // The same video continues; the new slide decides between looping and playing to
      // the end ("until the video ends").
      this.bg.watch?.setLoop(next.slide.duration.mode !== 'video');
      if (this.bg.video.ended || this.bg.ended) {
        // Ended or stuck just before the end: restart explicitly (single-video playlists).
        this.bg.ended = false;
        this.bg.video.currentTime = 0;
        this.bg.watch?.start();
      }
    }
    // Content (non-pinned elements).
    const handles: ElementHandle[] = [];
    const layer = document.createElement('div');
    layer.className = 'os-content';
    for (const e of next.slide.elements) {
      if (e.pinned) continue;
      const h = renderElement(e, next.env);
      handles.push(h);
      layer.appendChild(h.el);
    }
    const oldContent = this.content;
    this.content = { el: layer, handles };
    this.contentLayer.appendChild(layer);
    for (const h of handles) h.start();
    if (oldContent) {
      for (const h of oldContent.handles) h.stop();
      anims.push(this.transition(oldContent.el, layer, t).then(() => this.dropContent(oldContent)));
    }
    // Pinned elements: identical ones stay, others fade (never move).
    const wanted = next.slide.elements.filter((e) => e.pinned);
    const keep: PinnedNode[] = [];
    const used = new Set<PinnedNode>();
    for (const e of wanted) {
      const s = elementSignature(e);
      const existing = this.pinned.find((p) => p.sig === s && !used.has(p));
      if (existing) {
        used.add(existing);
        keep.push(existing);
        continue;
      }
      const h = renderElement(e, next.env);
      this.pinnedLayer.appendChild(h.el);
      h.start();
      if (this.pinned.length && t.type !== 'cut') anims.push(fade(h.el, 0, 1, t.ms));
      keep.push({ sig: s, handle: h });
    }
    for (const p of this.pinned) {
      if (used.has(p)) continue;
      p.handle.stop();
      const done = t.type === 'cut' ? Promise.resolve() : fade(p.handle.el, 1, 0, t.ms);
      anims.push(done.then(() => {
        p.handle.destroy();
        p.handle.el.remove();
      }));
    }
    this.pinned = keep;
    await Promise.all(anims);
    if (gen !== this.generation) return;
  }

  private async swapCanvas(next: StageSlide, t: Transition, gen: number): Promise<void> {
    const oldCanvas = this.canvas;
    const oldBg = this.bg;
    const oldContent = this.content;
    const oldPinned = this.pinned;
    oldBg?.watch?.stop();
    oldBg?.video?.pause();
    oldContent?.handles.forEach((h) => h.stop());
    oldPinned.forEach((p) => p.handle.stop());
    this.canvas = this.newCanvas();
    this.bgLayer = this.canvas.children[0] as HTMLElement;
    this.contentLayer = this.canvas.children[1] as HTMLElement;
    this.pinnedLayer = this.canvas.children[2] as HTMLElement;
    this.bg = null;
    this.content = null;
    this.pinned = [];
    this.design = { w: next.width, h: next.height };
    this.fit(this.canvas);
    this.root.appendChild(this.canvas);
    const shown = this.show(next);
    await Promise.all([shown, this.transition(oldCanvas, this.canvas, t)]);
    if (oldBg) this.dropBackground(oldBg);
    if (oldContent) this.dropContent(oldContent);
    oldPinned.forEach((p) => p.handle.destroy());
    oldCanvas.remove();
    if (gen !== this.generation) return;
  }

  private buildBackground(s: StageSlide): BgNode {
    const b = s.slide.background;
    const el = document.createElement('div');
    el.className = 'os-bg';
    el.style.backgroundColor = color(b.color, '#000');
    const node: BgNode = { sig: backgroundSignature(b), el };
    if (b.type === 'gradient') {
      const stops = b.stops.map((st) => `${color(st.color)} ${round(st.at * 100)}%`).join(', ');
      el.style.backgroundImage = `linear-gradient(${round(b.angle)}deg, ${stops})`;
    } else if (b.type === 'image') {
      const url = s.env.assets.url(b.assetId, s.env.mode === 'thumb' ? 'thumb' : 'display');
      if (url) el.style.backgroundImage = `url("${url}")`;
      el.style.backgroundSize = b.fit === 'fill' ? '100% 100%' : b.fit;
    } else if (b.type === 'video') {
      if (s.env.mode === 'play') {
        const untilEnd = s.slide.duration.mode === 'video';
        const v = createVideo(s.env.assets.url(b.assetId, 'display'), b.fit, !untilEnd, true);
        v.style.objectFit = objectFit(b.fit);
        const at = s.startAtMs;
        if (at && at > 0) {
          // Joining a running timeline: continue where the other screens are.
          v.addEventListener(
            'loadedmetadata',
            () => {
              const d = v.duration;
              if (!Number.isFinite(d) || d <= 0) return;
              v.currentTime = untilEnd ? Math.min(at / 1000, Math.max(0, d - 0.2)) : (at / 1000) % d;
            },
            { once: true },
          );
        }
        el.appendChild(v);
        node.video = v;
        node.watch = watchVideo(v, {
          loop: !untilEnd,
          onEnded: () => {
            node.ended = true;
            if (this.bg === node) this.events.onBackgroundEnded?.();
          },
          onFailed: (reason) => {
            if (this.bg === node) this.events.onMediaFailed?.(reason);
          },
        });
      } else {
        const url = s.env.assets.url(b.assetId, 'poster');
        if (url) el.style.backgroundImage = `url("${url}")`;
        el.style.backgroundSize = b.fit === 'fill' ? '100% 100%' : b.fit;
      }
    }
    return node;
  }

  private dropBackground(b: BgNode): void {
    b.watch?.stop();
    if (b.video) releaseVideo(b.video);
    b.el.remove();
  }

  private dropContent(c: ShownContent): void {
    for (const h of c.handles) h.destroy();
    c.el.remove();
  }

  /** Background video of the current slide (for the sequencer). */
  backgroundVideo(): HTMLVideoElement | undefined {
    return this.bg?.video;
  }

  private transition(from: HTMLElement, to: HTMLElement, t: Transition): Promise<void> {
    const ms = t.type === 'cut' ? 0 : Math.max(0, t.ms);
    if (ms === 0) {
      from.style.opacity = '0';
      return Promise.resolve();
    }
    const opts: KeyframeAnimationOptions = { duration: ms, easing: 'ease-in-out', fill: 'forwards' };
    const run = (el: HTMLElement, kf: Keyframe[]): Promise<void> =>
      el.animate(kf, opts).finished.then(
        () => undefined,
        () => undefined,
      );
    switch (t.type) {
      case 'fade':
        return Promise.all([run(to, [{ opacity: 0 }, { opacity: 1 }]), run(from, [{ opacity: 1 }, { opacity: 0 }])]).then(() => undefined);
      case 'zoom':
        return Promise.all([
          run(to, [{ opacity: 0, transform: 'scale(1.08)' }, { opacity: 1, transform: 'scale(1)' }]),
          run(from, [{ opacity: 1, transform: 'scale(1)' }, { opacity: 0, transform: 'scale(0.96)' }]),
        ]).then(() => undefined);
      case 'slide': {
        const d = t.direction ?? 'left';
        const axis = d === 'left' || d === 'right' ? 'X' : 'Y';
        const sign = d === 'left' || d === 'up' ? 1 : -1;
        const off = `${sign * SLIDE_OFFSET * 100}%`;
        const neg = `${-sign * SLIDE_OFFSET * 100}%`;
        return Promise.all([
          run(to, [{ transform: `translate${axis}(${off})` }, { transform: `translate${axis}(0)` }]),
          run(from, [{ transform: `translate${axis}(0)` }, { transform: `translate${axis}(${neg})` }]),
        ]).then(() => undefined);
      }
      default:
        from.style.opacity = '0';
        return Promise.resolve();
    }
  }

  /** Removes everything (black screen). */
  clear(): void {
    this.generation++;
    if (this.bg) this.dropBackground(this.bg);
    if (this.content) this.dropContent(this.content);
    for (const p of this.pinned) {
      p.handle.destroy();
      p.handle.el.remove();
    }
    this.bg = null;
    this.content = null;
    this.pinned = [];
    this.root.style.backgroundColor = '#000';
  }

  destroy(): void {
    this.clear();
    clearInterval(this.shiftTimer);
    this.resizeObs.disconnect();
    this.root.remove();
  }
}

function fade(el: HTMLElement, from: number, to: number, ms: number): Promise<void> {
  return el
    .animate([{ opacity: from }, { opacity: to }], { duration: Math.max(1, ms), fill: 'forwards' })
    .finished.then(
      () => undefined,
      () => undefined,
    );
}
