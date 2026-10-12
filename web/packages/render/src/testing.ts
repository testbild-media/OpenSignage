// Test doubles for browser APIs that jsdom lacks (Web Animations, ResizeObserver, layout
// boxes, media playback). Only imported by tests; never part of the player bundle.
// All timing goes through window.setTimeout/setInterval, so vitest fake timers drive it.
import { vi } from 'vitest';
import type { ElementBase, Presentation, Slide, TextStyle, Typography } from '@opensignage/contracts';
import type { AssetResolver, RenderEnv } from './env';
import { LiveStore } from './live';

// ---------------------------------------------------------------- fixtures

export const TEXT_STYLE: TextStyle = {
  font: 'Montserrat',
  size: 24,
  color: '#ffffff',
  bold: false,
  italic: false,
  underline: false,
  strike: false,
  letterSpacing: 0,
  lineHeight: 1.2,
  align: 'left',
};

export const TYPOGRAPHY: Typography = {
  heading: TEXT_STYLE,
  subheading: TEXT_STYLE,
  item: TEXT_STYLE,
  description: TEXT_STYLE,
  price: TEXT_STYLE,
};

/** Records which purpose (display / poster / thumb) was requested for an asset. */
export const TEST_ASSETS: AssetResolver = { url: (id, purpose) => `/media/${id}/${purpose}` };

export function testEnv(live: LiveStore = new LiveStore(), assets: AssetResolver = TEST_ASSETS): RenderEnv {
  return { typography: TYPOGRAPHY, assets, live, mode: 'play' };
}

export function base(id: string, patch: Partial<ElementBase> = {}): ElementBase {
  return {
    id,
    name: id,
    x: 0,
    y: 0,
    w: 100,
    h: 100,
    rotation: 0,
    opacity: 1,
    locked: false,
    hidden: false,
    pinned: false,
    ...patch,
  };
}

export function slide(id: string, patch: Partial<Slide> = {}): Slide {
  return {
    id,
    name: id,
    hidden: false,
    duration: { mode: 'fixed', ms: 10_000 },
    background: { type: 'color', color: '#000000' },
    transition: { type: 'cut', ms: 0 },
    elements: [],
    ...patch,
  };
}

export function presentation(id: string, slides: Slide[], width = 1920, height = 1080): Presentation {
  return { id, name: id, width, height, typography: TYPOGRAPHY, slides };
}

// ---------------------------------------------------------------- Web Animations

export interface AnimationCall {
  el: Element;
  keyframes: Keyframe[];
  options: KeyframeAnimationOptions;
  cancelled: boolean;
  finished: boolean;
  animation: Animation;
}

export interface FakeAnimations {
  calls: AnimationCall[];
  /** Calls made on el (or one of its descendants with deep = true). */
  on(el: Element, deep?: boolean): AnimationCall[];
  restore(): void;
}

/** Installs Element.prototype.animate; finite animations finish after duration × iterations. */
export function fakeAnimations(): FakeAnimations {
  const calls: AnimationCall[] = [];
  const proto = Element.prototype as unknown as { animate?: unknown };
  const had = Object.prototype.hasOwnProperty.call(proto, 'animate');
  const original = proto.animate;
  proto.animate = function animate(
    this: Element,
    keyframes: Keyframe[] | PropertyIndexedKeyframes | null,
    options?: number | KeyframeAnimationOptions,
  ): Animation {
    const opts: KeyframeAnimationOptions =
      typeof options === 'number' ? { duration: options } : (options ?? {});
    let resolve!: () => void;
    let reject!: (e: unknown) => void;
    const finished = new Promise<Animation>((res, rej) => {
      resolve = () => res(animation);
      reject = rej;
    });
    finished.catch(() => undefined); // a cancelled animation must not become an unhandled rejection
    let timer = 0;
    const call: AnimationCall = {
      el: this,
      keyframes: (keyframes ?? []) as Keyframe[],
      options: opts,
      cancelled: false,
      finished: false,
      animation: undefined as unknown as Animation,
    };
    const animation = {
      finished,
      cancel(): void {
        if (call.finished || call.cancelled) return;
        call.cancelled = true;
        clearTimeout(timer);
        reject(new DOMException('The animation was cancelled', 'AbortError'));
      },
    } as unknown as Animation;
    call.animation = animation;
    const iterations = opts.iterations ?? 1;
    if (Number.isFinite(iterations)) {
      timer = window.setTimeout(
        () => {
          call.finished = true;
          resolve();
        },
        Number(opts.duration ?? 0) * iterations,
      );
    }
    calls.push(call);
    return animation;
  };
  return {
    calls,
    on(el, deep = false) {
      return calls.filter((c) => c.el === el || (deep && el.contains(c.el)));
    },
    restore() {
      if (had) proto.animate = original;
      else delete proto.animate;
    },
  };
}

// ---------------------------------------------------------------- ResizeObserver and boxes

export interface FakeResizeObservers {
  /** Notifies every observer (as the browser does after a viewport change). */
  resize(): void;
}

/** Stubs the global ResizeObserver (restore with vi.unstubAllGlobals()). */
export function fakeResizeObserver(): FakeResizeObservers {
  const all = new Set<{ cb: ResizeObserverCallback; self: ResizeObserver; targets: Set<Element> }>();
  class FakeResizeObserver {
    private entry: { cb: ResizeObserverCallback; self: ResizeObserver; targets: Set<Element> };
    constructor(cb: ResizeObserverCallback) {
      this.entry = { cb, self: this as unknown as ResizeObserver, targets: new Set() };
      all.add(this.entry);
    }
    observe(t: Element): void {
      this.entry.targets.add(t);
    }
    unobserve(t: Element): void {
      this.entry.targets.delete(t);
    }
    disconnect(): void {
      all.delete(this.entry);
    }
  }
  vi.stubGlobal('ResizeObserver', FakeResizeObserver);
  return {
    resize() {
      for (const o of [...all]) if (o.targets.size) o.cb([], o.self);
    },
  };
}

function rect(width: number, height: number): DOMRect {
  return {
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: width,
    bottom: height,
    width,
    height,
    toJSON: () => ({}),
  } as DOMRect;
}

/** Gives one element a fixed layout box (e.g. the stage root = the screen). */
export function setBox(el: Element, width: number, height: number): void {
  el.getBoundingClientRect = () => rect(width, height);
}

/**
 * Simple text metrics for every element: measure(el) returns a size or null (= 0×0, as
 * jsdom). Used by the ticker, which measures one text copy and the host width.
 */
export function fakeLayout(
  measure: (el: HTMLElement) => { width: number; height: number } | null,
): () => void {
  const proto = HTMLElement.prototype;
  const original = Object.getOwnPropertyDescriptor(Element.prototype, 'getBoundingClientRect')!;
  proto.getBoundingClientRect = function getBoundingClientRect(this: HTMLElement): DOMRect {
    const m = measure(this);
    return m ? rect(m.width, m.height) : (original.value as () => DOMRect).call(this);
  };
  return () => {
    delete (proto as Partial<HTMLElement>).getBoundingClientRect;
  };
}

// ---------------------------------------------------------------- media playback

export interface MediaState {
  currentTime: number;
  duration: number;
  paused: boolean;
  ended: boolean;
  plays: number;
  pauses: number;
  loads: number;
  /** Simulated decoder hang: the position stops advancing here (null = plays normally). */
  freezeAt: number | null;
  /** A reload (load()) cures the hang. */
  healOnLoad: boolean;
  /** play() rejects (broken file); 'abort' rejects as interrupted by a new load. */
  rejectPlay: boolean | 'abort';
  /** The decoder stops at the end without firing "ended" (paused, never reports the end). */
  swallowEnded: boolean;
}

export interface FakeMedia {
  state(v: HTMLMediaElement): MediaState;
  /** All media elements play() was called on, in order. */
  started: HTMLMediaElement[];
  /** Initial state of media elements created from now on (e.g. { rejectPlay: true }). */
  defaults: Partial<MediaState>;
  restore(): void;
}

const MEDIA_PROPS = ['currentTime', 'duration', 'paused', 'ended', 'readyState'] as const;
const MEDIA_METHODS = ['play', 'pause', 'load'] as const;

/**
 * Replaces HTMLMediaElement playback with a simulation driven by window.setInterval:
 * playing elements advance their position in real (fake-timer) time, wrap around when
 * loop is set and otherwise stop and fire "ended" — like Chromium.
 */
export function fakeMedia(opts: { durationFor?: (src: string) => number; tickMs?: number } = {}): FakeMedia {
  const tickMs = opts.tickMs ?? 250;
  const states = new WeakMap<HTMLMediaElement, MediaState>();
  const started: HTMLMediaElement[] = [];
  const defaults: Partial<MediaState> = {};
  const proto = HTMLMediaElement.prototype;
  const saved = new Map<string, PropertyDescriptor>();
  for (const k of [...MEDIA_PROPS, ...MEDIA_METHODS])
    saved.set(k, Object.getOwnPropertyDescriptor(proto, k)!);

  const state = (v: HTMLMediaElement): MediaState => {
    let s = states.get(v);
    if (!s) {
      s = {
        currentTime: 0,
        duration: opts.durationFor?.(v.getAttribute('src') ?? '') ?? 10,
        paused: true,
        ended: false,
        plays: 0,
        pauses: 0,
        loads: 0,
        freezeAt: null,
        healOnLoad: false,
        rejectPlay: false,
        swallowEnded: false,
        ...defaults,
      };
      states.set(v, s);
    }
    return s;
  };

  Object.defineProperty(proto, 'currentTime', {
    configurable: true,
    get(this: HTMLMediaElement) {
      return state(this).currentTime;
    },
    set(this: HTMLMediaElement, t: number) {
      const s = state(this);
      s.currentTime = t;
      s.ended = false;
    },
  });
  Object.defineProperty(proto, 'duration', {
    configurable: true,
    get(this: HTMLMediaElement) {
      return this.getAttribute('src') ? state(this).duration : NaN;
    },
  });
  Object.defineProperty(proto, 'paused', {
    configurable: true,
    get(this: HTMLMediaElement) {
      return state(this).paused;
    },
  });
  Object.defineProperty(proto, 'ended', {
    configurable: true,
    get(this: HTMLMediaElement) {
      return state(this).ended;
    },
  });
  Object.defineProperty(proto, 'readyState', {
    configurable: true,
    get(this: HTMLMediaElement) {
      return this.getAttribute('src') ? 4 : 0;
    },
  });
  Object.defineProperty(proto, 'play', {
    configurable: true,
    writable: true,
    value(this: HTMLMediaElement): Promise<void> {
      const s = state(this);
      s.plays++;
      if (!started.includes(this)) started.push(this);
      if (s.rejectPlay === 'abort') {
        s.rejectPlay = false; // only the first call is interrupted
        return Promise.reject(new DOMException('The play() request was interrupted by a new load request.', 'AbortError'));
      }
      if (s.rejectPlay || !this.getAttribute('src')) {
        return Promise.reject(new DOMException('The element has no supported sources.', 'NotSupportedError'));
      }
      if (s.ended) {
        s.currentTime = 0;
        s.ended = false;
      }
      s.paused = false;
      return Promise.resolve();
    },
  });
  Object.defineProperty(proto, 'pause', {
    configurable: true,
    writable: true,
    value(this: HTMLMediaElement): void {
      const s = state(this);
      s.pauses++;
      s.paused = true;
    },
  });
  Object.defineProperty(proto, 'load', {
    configurable: true,
    writable: true,
    value(this: HTMLMediaElement): void {
      const s = state(this);
      s.loads++;
      s.paused = true;
      s.ended = false;
      s.currentTime = 0;
      if (s.healOnLoad) s.freezeAt = null;
    },
  });

  const timer = window.setInterval(() => {
    for (const v of started) {
      const s = state(v);
      if (s.paused || s.ended) continue;
      let t = s.currentTime + tickMs / 1000;
      if (s.freezeAt !== null) t = Math.min(t, Math.max(s.currentTime, s.freezeAt));
      if (t >= s.duration) {
        if (v.loop) {
          t -= s.duration;
        } else {
          s.currentTime = s.duration;
          s.paused = true;
          if (!s.swallowEnded) {
            s.ended = true;
            v.dispatchEvent(new Event('ended'));
          }
          continue;
        }
      }
      s.currentTime = t;
    }
  }, tickMs);

  return {
    state,
    started,
    defaults,
    restore() {
      clearInterval(timer);
      for (const [k, d] of saved) Object.defineProperty(proto, k, d);
    },
  };
}
