import type { Fit, Manifest, Playlist, PlaylistItem, Presentation, Slide } from '@opensignage/contracts';
import { LIMITS } from '@opensignage/contracts';
import type { RenderEnv } from './env';
import type { Stage } from './stage';
import { buildTimeline, locate, playlistTracks } from './timeline';

export interface MediaInfo {
  kind: 'image' | 'video' | 'audio' | 'font';
  durationMs?: number;
}

/** Content the sequencer can play (built from a deployment manifest or editor data). */
export interface SequencerContent {
  presentations: Map<string, Presentation>;
  playlists: Map<string, Playlist>;
  media: Map<string, MediaInfo>;
}

export function contentFromManifest(m: Manifest): SequencerContent {
  const media = new Map<string, MediaInfo>();
  for (const a of m.assets) {
    if (!media.has(a.id)) media.set(a.id, { kind: a.kind, durationMs: a.durationMs });
  }
  return {
    presentations: new Map(m.presentations.map((p) => [p.id, p])),
    playlists: new Map(m.playlists.map((p) => [p.id, p])),
    media,
  };
}

export interface SequencerHooks {
  /** Called whenever a slide becomes current (e.g. to hide the zone ticker overlay). */
  onSlide?: (slide: Slide, description: string) => void;
  report?: (level: 'warn' | 'error', msg: string) => void;
  /** Clock for synchronous playback (server-corrected on screens; default Date.now). */
  now?: () => number;
}

/** A step that would end within this time is skipped: no flash of a slide on joining. */
const MIN_STEP_REST_MS = 200;
/** A late joiner seeks the background video only when it is this far in (else starts at 0). */
const SEEK_FROM_MS = 300;
/** A step starts at most this much early to compensate its build time. */
const MAX_LEAD_MS = 400;
/** Waking up later than this after a step boundary counts as having fallen behind. */
const RESYNC_AFTER_MS = 1000;

const MEDIA_TRANSITION = { type: 'fade', ms: 500 } as const;
const VIDEO_SAFETY_EXTRA_MS = 120_000; // ~2 min after the expected end
const DEFAULT_IMAGE_MS = 10_000;

class Cancelled extends Error {}

/**
 * Plays playlists on a stage: presentations, single slides, media files; duration (a
 * started pass is finished, not cut) or repeat count (exactly N passes); loops forever.
 * Timers are never tied to polling; new content only restarts playback when called.
 */
export class Sequencer {
  private content: SequencerContent = { presentations: new Map(), playlists: new Map(), media: new Map() };
  private playlistId: string | null = null;
  /** Start of the timeline (ms epoch): synchronous playback; null = play from the start. */
  private anchor: number | null = null;
  /** Place of the screen in its group: the track of a playlist for several screens. */
  private position = 1;
  private gen = 0;
  private wake: (() => void) | null = null;
  private bgEnded = false;
  private bgFailed = false;
  private counter = 0;

  constructor(
    private stage: Stage,
    private envFor: (p: Presentation | null) => RenderEnv,
    private hooks: SequencerHooks = {},
  ) {
    stage.events.onBackgroundEnded = () => {
      this.bgEnded = true;
      this.wake?.();
    };
    stage.events.onMediaFailed = (reason) => {
      this.hooks.report?.('warn', `media failed: ${reason}`);
      this.bgFailed = true;
      this.wake?.();
    };
  }

  /** Replaces content and restarts the current playlist (if any). */
  setContent(c: SequencerContent): void {
    this.content = c;
    this.restart();
  }

  /**
   * Switches to a playlist (null = nothing: clear the screen). With an anchor (ms epoch)
   * every screen plays the same position of the same timeline at the same
   * time: position = (now − anchor) mod cycle; without one the playlist starts from its
   * first item (editor preview). `position` chooses the track of a playlist for several
   * screens. Same id, anchor and position = no-op.
   */
  play(playlistId: string | null, anchor: number | null = null, position = 1): void {
    if (playlistId === this.playlistId && anchor === this.anchor && position === this.position) return;
    this.playlistId = playlistId;
    this.anchor = anchor;
    this.position = position;
    this.restart();
  }

  private now(): number {
    return this.hooks.now ? this.hooks.now() : Date.now();
  }

  current(): string | null {
    return this.playlistId;
  }

  stop(): void {
    this.gen++;
    this.wake?.();
  }

  private restart(): void {
    const gen = ++this.gen;
    this.wake?.();
    if (!this.playlistId) {
      this.stage.clear();
      return;
    }
    void this.loop(gen).catch((e) => {
      if (!(e instanceof Cancelled)) this.hooks.report?.('error', `sequencer crashed: ${String(e)}`);
    });
  }

  private check(gen: number): void {
    if (gen !== this.gen) throw new Cancelled();
  }

  private async loop(gen: number): Promise<void> {
    if (this.anchor !== null) return this.syncLoop(gen, this.anchor);
    const tracks = this.playlistId ? this.content.playlists.get(this.playlistId) : undefined;
    if (tracks && playlistTracks(tracks) > 1) return this.syncLoop(gen, this.now()); // steps with fixed durations
    while (true) {
      this.check(gen);
      const pl = this.playlistId ? this.content.playlists.get(this.playlistId) : undefined;
      if (!pl || pl.items.length === 0) {
        this.stage.clear();
        return;
      }
      let shown = 0;
      for (const item of pl.items) {
        this.check(gen);
        shown += await this.playItem(item, gen);
      }
      if (shown === 0) {
        // Nothing playable (all references missing): stay black and retry later.
        this.stage.clear();
        await this.sleep(5000, gen);
      }
    }
  }

  /**
   * Synchronous playback: each step is computed from the clock (never from the previous
   * step), so slow loading or a late start never shifts a screen against the others.
   */
  private async syncLoop(gen: number, anchor: number): Promise<void> {
    const pl = this.playlistId ? this.content.playlists.get(this.playlistId) : undefined;
    const tl = pl ? buildTimeline(pl, { content: this.content, passes: (i) => this.passes(i) }, this.position) : null;
    if (!tl || tl.steps.length === 0) {
      if (pl && pl.items.length > 0) this.hooks.report?.('warn', `playlist ${pl.id} has nothing playable`);
      this.stage.clear();
      return;
    }
    // Building a slide (DOM, product lists, video element) takes time – up to ~200 ms on a
    // Raspberry Pi 3. Each step remembers how long its last builds took and starts that much
    // earlier the next time (the shortest of the last three: the first, cold build with
    // fonts and images loading is much slower and must not make later ones early).
    const build = new Map<number, number[]>();
    let at = this.now(); // when the step should be visible
    let first = true;
    while (true) {
      this.check(gen);
      // Fell far behind (busy device, timers held back): rejoin like a screen that starts now.
      const behind = this.now() - at > RESYNC_AFTER_MS;
      if (behind) at = this.now();
      let pos = locate(tl, anchor, at)!;
      if ((first || behind) && pos.endAt - at < MIN_STEP_REST_MS) {
        at = pos.endAt;
        pos = locate(tl, anchor, at)!;
      }
      const step = tl.steps[pos.index]!;
      // A screen that joins in the middle (start, restart, new content) seeks a running
      // background video to where the other screens are.
      const bgAt = at - (pos.startAt - step.start + step.bgStart);
      const startAtMs = (first || behind) && bgAt > SEEK_FROM_MS ? bgAt : undefined;
      first = false;
      const t0 = this.now();
      const shown = this.stage.show({ slide: step.slide, width: step.pres.width, height: step.pres.height, env: this.envFor(step.pres), key: `${step.slide.id}#${++this.counter}`, startAtMs });
      const took = this.now() - t0; // the synchronous part: the new slide is in the DOM now
      build.set(pos.index, [...(build.get(pos.index) ?? []), took].slice(-3));
      await shown;
      this.check(gen);
      this.hooks.onSlide?.(step.slide, `${step.pres.name || 'media'} / ${step.slide.name || step.slide.id}`);
      at = pos.endAt;
      const next = locate(tl, anchor, at)!;
      const lead = Math.min(MAX_LEAD_MS, ...(build.get(next.index) ?? [0]));
      await this.sleep(at - lead - this.now(), gen);
    }
  }

  /** Plays one playlist item; returns the number of slides shown. */
  private async playItem(item: PlaylistItem, gen: number): Promise<number> {
    const passes = this.passes(item);
    if (!passes) {
      this.hooks.report?.('warn', `playlist item ${item.id} (${item.kind} ${item.refId}) not playable`);
      return 0;
    }
    const { pres, slides } = passes;
    if (slides.length === 0) return 0;
    let shown = 0;
    const started = Date.now();
    const play = item.play;
    for (let pass = 0; ; pass++) {
      if (play.mode === 'repeat' && pass >= Math.max(1, play.count)) break;
      if (play.mode === 'duration' && pass > 0 && Date.now() - started >= play.ms) break;
      for (const s of slides) {
        this.check(gen);
        await this.showSlide(s, pres, gen);
        shown++;
      }
      if (play.mode === 'duration' && play.ms <= 0) break;
    }
    return shown;
  }

  private passes(item: PlaylistItem): { pres: Presentation; slides: Slide[] } | null {
    const c = this.content;
    if (item.kind === 'presentation') {
      const p = c.presentations.get(item.refId);
      if (!p) return null;
      return { pres: p, slides: p.slides.filter((s) => !s.hidden) };
    }
    if (item.kind === 'slide') {
      const p = item.presentationId ? c.presentations.get(item.presentationId) : [...c.presentations.values()].find((x) => x.slides.some((s) => s.id === item.refId));
      const s = p?.slides.find((x) => x.id === item.refId);
      if (!p || !s) return null;
      return { pres: p, slides: [s] };
    }
    const info = c.media.get(item.refId);
    if (!info || (info.kind !== 'image' && info.kind !== 'video')) return null;
    const pres = this.mediaPresentation(item, info);
    return { pres, slides: pres.slides };
  }

  /** Wraps a media file into a virtual one-slide presentation at the screen's aspect. */
  private mediaPresentation(item: PlaylistItem, info: MediaInfo): Presentation {
    const r = this.stage.root.getBoundingClientRect();
    const h = 1080;
    const w = r.width > 0 && r.height > 0 ? Math.round((h * r.width) / r.height) : 1920;
    const fit: Fit = item.fit ?? 'contain';
    const isVideo = info.kind === 'video';
    const slide: Slide = {
      id: `media-${item.id}`,
      name: '',
      hidden: false,
      duration: isVideo
        ? { mode: 'video', maxMs: (info.durationMs ?? LIMITS.defaultVideoMaxMs) + VIDEO_SAFETY_EXTRA_MS }
        : { mode: 'fixed', ms: item.play.mode === 'duration' ? item.play.ms : DEFAULT_IMAGE_MS },
      background: isVideo
        ? { type: 'video', color: '#000000', assetId: item.refId, fit }
        : { type: 'image', color: '#000000', assetId: item.refId, fit },
      transition: { ...MEDIA_TRANSITION },
      elements: [],
    };
    const empty = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' as const };
    return { id: `media-${item.id}`, name: '', width: w, height: h, slides: [slide], typography: { heading: empty, subheading: empty, item: empty, description: empty, price: empty } };
  }

  private async showSlide(slide: Slide, pres: Presentation, gen: number): Promise<void> {
    this.bgEnded = false;
    this.bgFailed = false;
    const started = Date.now();
    await this.stage.show({ slide, width: pres.width, height: pres.height, env: this.envFor(pres), key: `${slide.id}#${++this.counter}` });
    this.check(gen);
    this.hooks.onSlide?.(slide, `${pres.name || 'media'} / ${slide.name || slide.id}`);
    if (slide.duration.mode === 'fixed') {
      await this.sleep(Math.max(LIMITS.minSlideMs, slide.duration.ms) - (Date.now() - started), gen);
      return;
    }
    // Until the background video ends, with a safety cap and media failure handling.
    const deadline = started + this.videoCap(slide);
    while (!this.bgEnded) {
      this.check(gen);
      if (this.bgFailed) {
        await this.sleep(3000, gen);
        return;
      }
      const left = deadline - Date.now();
      if (left <= 0) {
        this.hooks.report?.('warn', `video slide ${slide.id} hit its safety timer`);
        return;
      }
      await this.sleep(Math.min(left, 1000), gen);
    }
  }

  /** Safety timer of a slide "until the video ends": its maxMs, but at most ~2 min after
   * the known length of the background video (a video that never reports
   * its end must not block the screen for the 30 min default). */
  private videoCap(slide: Slide): number {
    const d = slide.duration;
    const cap = (d.mode === 'video' && d.maxMs) || LIMITS.defaultVideoMaxMs;
    const b = slide.background;
    const length = b.type === 'video' ? this.content.media.get(b.assetId)?.durationMs : undefined;
    return length && length > 0 ? Math.min(cap, length + VIDEO_SAFETY_EXTRA_MS) : cap;
  }

  private sleep(ms: number, gen: number): Promise<void> {
    return new Promise((resolve, reject) => {
      if (gen !== this.gen) {
        reject(new Cancelled());
        return;
      }
      const done = (): void => {
        clearTimeout(timer);
        this.wake = null;
        if (gen !== this.gen) reject(new Cancelled());
        else resolve();
      };
      const timer = window.setTimeout(done, Math.max(0, ms));
      this.wake = done;
    });
  }
}
