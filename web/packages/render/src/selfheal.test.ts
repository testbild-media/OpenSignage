// Failure scenario "video hangs or never ends → self-healing after ~2 min at the latest":
// real Stage + Sequencer + video watch on a simulated decoder.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Background, PlaylistItem, Presentation, Slide } from '@opensignage/contracts';
import { Stage } from './stage';
import { Sequencer, type MediaInfo, type SequencerContent } from './sequencer';
import {
  fakeAnimations,
  fakeMedia,
  fakeResizeObserver,
  presentation,
  setBox,
  slide,
  testEnv,
  type FakeAnimations,
  type FakeMedia,
} from './testing';

const CLIP: Background = { type: 'video', color: '#000000', assetId: 'clip', fit: 'cover' };
const CLIP_MS = 60_000;

let anims: FakeAnimations;
let media: FakeMedia;
let stage: Stage;
let sequencer: Sequencer;
let shown: { id: string; at: number }[];
let reports: string[];
let t0: number;

beforeEach(() => {
  vi.useFakeTimers();
  anims = fakeAnimations();
  fakeResizeObserver();
  media = fakeMedia({ durationFor: () => CLIP_MS / 1000 });
  const host = document.createElement('div');
  document.body.replaceChildren(host);
  stage = new Stage(host, 'contain');
  setBox(stage.root, 1920, 1080);
  shown = [];
  reports = [];
});

afterEach(() => {
  sequencer?.stop();
  stage.destroy();
  media.restore();
  anims.restore();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function play(
  presentations: Presentation[],
  items: PlaylistItem[],
  mediaInfo: [string, MediaInfo][] = [['clip', { kind: 'video', durationMs: CLIP_MS }]],
): void {
  const content: SequencerContent = {
    presentations: new Map(presentations.map((p) => [p.id, p])),
    playlists: new Map([['L', { id: 'L', name: 'L', items }]]),
    media: new Map(mediaInfo),
  };
  sequencer = new Sequencer(stage, () => testEnv(), {
    onSlide: (s) => shown.push({ id: s.id, at: Date.now() - t0 }),
    report: (_level, msg) => reports.push(msg),
  });
  sequencer.setContent(content);
  t0 = Date.now();
  sequencer.play('L');
}

const once = (refId: string): PlaylistItem => ({
  id: `i-${refId}`,
  kind: 'presentation',
  refId,
  play: { mode: 'repeat', count: 1 },
});

/** A presentation: a slide "until the video ends" followed by a 10 s slide. */
function videoThenNext(duration: Slide['duration'] = { mode: 'video' }): Presentation {
  return presentation('P', [
    slide('video', { background: CLIP, duration }),
    slide('next', { duration: { mode: 'fixed', ms: 10_000 } }),
  ]);
}

/** Time (ms after start) at which the slide was shown the n-th time. */
function shownAt(id: string, n = 0): number | undefined {
  return shown.filter((s) => s.id === id)[n]?.at;
}

function video(): HTMLVideoElement {
  return stage.backgroundVideo()!;
}

describe('video slides end with the video', () => {
  it('a slide "until the video ends" switches when the video ends', async () => {
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(CLIP_MS + 2000);
    expect(shownAt('video')).toBe(0);
    expect(shownAt('next')).toBeGreaterThanOrEqual(CLIP_MS);
    expect(shownAt('next')).toBeLessThanOrEqual(CLIP_MS + 1000);
    expect(reports).toEqual([]);
  });

  it('honours maxMs of a "until the video ends" slide', async () => {
    play([videoThenNext({ mode: 'video', maxMs: 20_000 })], [once('P')]);
    await vi.advanceTimersByTimeAsync(25_000);
    expect(shownAt('next')).toBeGreaterThanOrEqual(20_000);
    expect(shownAt('next')).toBeLessThanOrEqual(21_000);
    expect(reports.some((r) => r.includes('safety timer'))).toBe(true);
  });
});

describe('self-healing within ~2 minutes', () => {
  it('a video that stalls for good is reloaded, then skipped', async () => {
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    const v = video();
    media.state(v).freezeAt = 10; // decoder stops delivering frames at 0:10
    await vi.advanceTimersByTimeAsync(120_000);
    const at = shownAt('next')!;
    expect(at).toBeDefined();
    expect(at).toBeLessThanOrEqual(10_000 + 120_000);
    expect(at).toBeLessThan(CLIP_MS); // long before the video would have ended
    expect(media.state(v).loads).toBeGreaterThanOrEqual(1); // restart attempts first
    expect(reports).toContain('media failed: stalled');
  });

  it('a stall that a reload cures continues at the same position', async () => {
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    const v = video();
    Object.assign(media.state(v), { freezeAt: 10, healOnLoad: true });
    await vi.advanceTimersByTimeAsync(20_000);
    expect(media.state(v).loads).toBe(1);
    expect(media.state(v).currentTime).toBeGreaterThan(10); // resumed at 0:10, not at 0:00
    expect(shownAt('next')).toBeUndefined();
    await vi.advanceTimersByTimeAsync(CLIP_MS);
    expect(shownAt('next')).toBeDefined();
    expect(reports).toEqual([]);
  });

  it('a video stuck just before its end (Pi 4 decoder) counts as ended', async () => {
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    media.state(video()).freezeAt = CLIP_MS / 1000 - 0.5;
    await vi.advanceTimersByTimeAsync(CLIP_MS + 10_000);
    const at = shownAt('next')!;
    expect(at).toBeGreaterThan(CLIP_MS - 1000);
    expect(at).toBeLessThanOrEqual(CLIP_MS + 10_000);
  });

  it('a presentation video that never reports its end is skipped ~2 min after its length', async () => {
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    media.state(video()).swallowEnded = true; // stops on the last frame, no "ended" event
    await vi.advanceTimersByTimeAsync(CLIP_MS + 120_000 + 1000);
    const at = shownAt('next')!;
    expect(at).toBeDefined();
    expect(at).toBeGreaterThanOrEqual(CLIP_MS);
    expect(at).toBeLessThanOrEqual(CLIP_MS + 120_000 + 1000);
  });

  it('a video file in the playlist that never reports its end is skipped ~2 min after its length', async () => {
    const pres = presentation('P', [slide('next')]);
    play([pres], [{ id: 'm', kind: 'media', refId: 'clip', play: { mode: 'repeat', count: 1 } }, once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(shownAt('media-m')).toBe(0);
    media.state(video()).swallowEnded = true;
    await vi.advanceTimersByTimeAsync(CLIP_MS + 120_000 + 1000);
    expect(shownAt('next')).toBeDefined();
    expect(shownAt('next')).toBeLessThanOrEqual(CLIP_MS + 120_000 + 1000);
  });

  it('an interrupted play() (AbortError) is no failure: the video keeps its slide', async () => {
    media.defaults.rejectPlay = 'abort';
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(5000);
    expect(reports.filter((r) => r.includes('play_rejected'))).toEqual([]);
    expect(shownAt('next')).toBeUndefined();
  });

  it('broken media (play rejected) is skipped after a few seconds', async () => {
    media.defaults.rejectPlay = true;
    play([videoThenNext()], [once('P')]);
    await vi.advanceTimersByTimeAsync(5000);
    expect(shownAt('next')).toBeLessThanOrEqual(3000);
    expect(reports).toContain('media failed: play_rejected');
  });
});

describe('looping without freezing', () => {
  it('a playlist with a single video restarts the same video cleanly', async () => {
    play([], [{ id: 'm', kind: 'media', refId: 'clip', play: { mode: 'repeat', count: 1 } }]);
    await vi.advanceTimersByTimeAsync(1000);
    const v = video();
    await vi.advanceTimersByTimeAsync(CLIP_MS + 1000);
    expect(shownAt('media-m', 1)).toBeDefined();
    expect(video()).toBe(v); // same element, no flicker and no second decoder
    const s = media.state(v);
    expect(s.paused).toBe(false);
    expect(s.currentTime).toBeLessThan(5); // playing again from the start
    expect(s.plays).toBe(2);
  });

  it('a single video stuck before its end is restarted, not frozen on the last frame', async () => {
    play([], [{ id: 'm', kind: 'media', refId: 'clip', play: { mode: 'repeat', count: 1 } }]);
    await vi.advanceTimersByTimeAsync(1000);
    const v = video();
    // The decoder stops 0.5 s before the end on every pass and never fires "ended".
    media.state(v).freezeAt = CLIP_MS / 1000 - 0.5;
    await vi.advanceTimersByTimeAsync(CLIP_MS + 10_000);
    expect(shownAt('media-m', 1)).toBeDefined();
    await vi.advanceTimersByTimeAsync(3000);
    expect(video()).toBe(v);
    expect(media.state(v).currentTime).toBeLessThan(15); // playing again from the start
    expect(media.state(v).paused).toBe(false);
  });

  it('a decode error (hardware decoder gave up) is cured by a reload at the same position', async () => {
    // Seen on a Raspberry Pi 3 with V4L2 decoding: PIPELINE_ERROR_DECODE left the shared
    // background paused for good on fixed-duration slides.
    const pres = presentation('P', [
      slide('menu', { background: CLIP, duration: { mode: 'fixed', ms: 30_000 } }),
    ]);
    play([pres], [once('P')]);
    await vi.advanceTimersByTimeAsync(5000);
    const v = video();
    const at = media.state(v).currentTime;
    const loadsBefore = media.state(v).loads;
    // error until the next load(), like Chromium (load() starts a new pipeline)
    Object.defineProperty(v, 'error', { configurable: true, get: () => (media.state(v).loads > loadsBefore ? null : { code: 3 }) });
    media.state(v).paused = true;
    v.dispatchEvent(new Event('error'));
    await vi.advanceTimersByTimeAsync(2000);
    expect(media.state(v).loads).toBe(loadsBefore + 1);
    expect(media.state(v).paused).toBe(false);
    expect(media.state(v).currentTime).toBeGreaterThanOrEqual(at);
    expect(video()).toBe(v);
    expect(reports).toEqual([]);
  });

  it('a video that keeps failing is reported, then retried once a minute', async () => {
    const pres = presentation('P', [
      slide('menu', { background: CLIP, duration: { mode: 'fixed', ms: 600_000 } }),
    ]);
    play([pres], [once('P')]);
    await vi.advanceTimersByTimeAsync(2000);
    const v = video();
    Object.defineProperty(v, 'error', { configurable: true, get: () => ({ code: 3 }) });
    for (let i = 0; i < 4; i++) {
      media.state(v).paused = true;
      v.dispatchEvent(new Event('error'));
      await vi.advanceTimersByTimeAsync(500);
    }
    expect(media.state(v).loads).toBe(3); // three recoveries, then reported
    expect(reports.some((r) => r.includes('media_error_3'))).toBe(true);
    await vi.advanceTimersByTimeAsync(61_000);
    expect(media.state(v).loads).toBe(4); // retried after the window, never given up
  });

  it('a looping background keeps recovering from stalls over a long run', async () => {
    // One slide with a looping background video, repeated for hours (24 h soak test).
    const pres = presentation('P', [
      slide('menu', { background: CLIP, duration: { mode: 'fixed', ms: 30_000 } }),
    ]);
    play([pres], [once('P')]);
    await vi.advanceTimersByTimeAsync(1000);
    const v = video();
    for (let i = 0; i < 6; i++) {
      // A stall every 10 minutes, each cured by a reload.
      await vi.advanceTimersByTimeAsync(10 * 60_000);
      Object.assign(media.state(v), { freezeAt: media.state(v).currentTime, healOnLoad: true });
      await vi.advanceTimersByTimeAsync(15_000);
      expect(media.state(v).freezeAt).toBeNull(); // reloaded
    }
    expect(video()).toBe(v);
    expect(media.state(v).loads).toBe(6);
    expect(media.state(v).paused).toBe(false);
    expect(reports).toEqual([]);
  });

  it('a shared background video ends a following "until the video ends" slide', async () => {
    const pres = presentation('P', [
      slide('intro', { background: CLIP, duration: { mode: 'fixed', ms: 10_000 } }),
      slide('main', { background: { ...CLIP }, duration: { mode: 'video' } }),
      slide('next', { duration: { mode: 'fixed', ms: 10_000 } }),
    ]);
    play([pres], [once('P')]);
    await vi.advanceTimersByTimeAsync(CLIP_MS + 2000);
    expect(shownAt('main')).toBe(10_000);
    // The background continued from the intro slide and the slide ends with the video.
    expect(shownAt('next')).toBeGreaterThanOrEqual(CLIP_MS);
    expect(shownAt('next')).toBeLessThanOrEqual(CLIP_MS + 1000);
  });

  it('a shared background video keeps looping on a following fixed slide', async () => {
    const pres = presentation('P', [
      slide('main', { background: CLIP, duration: { mode: 'video' } }),
      slide('outro', { background: { ...CLIP }, duration: { mode: 'fixed', ms: 90_000 } }),
    ]);
    play([pres], [once('P')]);
    await vi.advanceTimersByTimeAsync(CLIP_MS + 1000);
    const outroAt = shownAt('outro')!;
    expect(outroAt).toBeGreaterThanOrEqual(CLIP_MS);
    const v = video();
    await vi.advanceTimersByTimeAsync(75_000); // past the end of the (restarted) video
    // The video loops on the fixed slide, and the slide keeps its 90 s.
    expect(video()).toBe(v);
    expect(media.state(v).paused).toBe(false);
    expect(shownAt('main', 1)).toBeUndefined();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(shownAt('main', 1)).toBe(outroAt + 90_000);
  });
});
