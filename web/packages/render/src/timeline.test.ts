import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Playlist, Slide } from '@opensignage/contracts';
import { Sequencer, type SequencerContent } from './sequencer';
import type { Stage, StageSlide } from './stage';
import { buildTimeline, locate } from './timeline';
import { presentation, slide, testEnv } from './testing';

const fixed = (id: string, ms: number, patch: Partial<Slide> = {}): Slide => slide(id, { duration: { mode: 'fixed', ms }, ...patch });

// P: a (5 s), b (5 s, video background), c (until the video ends: 12 s video); a media
// video item of 8 s and an image item.
const pres = presentation('P', [
  fixed('a', 5000),
  fixed('b', 5000, { background: { type: 'video', color: '#000000', assetId: 'v12', fit: 'cover' } }),
  slide('c', { duration: { mode: 'video', maxMs: 60_000 }, background: { type: 'video', color: '#000000', assetId: 'v12', fit: 'cover' } }),
]);
const content = (pl: Playlist): SequencerContent => ({
  presentations: new Map([['P', pres]]),
  playlists: new Map([[pl.id, pl]]),
  media: new Map([
    ['v12', { kind: 'video', durationMs: 12_000 }],
    ['v8', { kind: 'video', durationMs: 8000 }],
    ['img', { kind: 'image' }],
  ]),
});

class FakeStage {
  shown: { id: string; at: number; startAtMs?: number }[] = [];
  root = document.createElement('div');
  events: { onBackgroundEnded?: () => void; onMediaFailed?: (r: string) => void } = {};
  async show(s: StageSlide): Promise<void> {
    this.shown.push({ id: s.slide.id, at: Date.now(), startAtMs: s.startAtMs });
  }
  clear(): void {
    this.shown.push({ id: 'CLEAR', at: Date.now() });
  }
}

function source(pl: Playlist) {
  const seq = new Sequencer(new FakeStage() as unknown as Stage, () => testEnv());
  seq.setContent(content(pl));
  return { content: content(pl), passes: (i: Playlist['items'][number]) => (seq as unknown as { passes(i: unknown): ReturnType<typeof seq['passes']> }).passes(i) };
}

describe('timeline', () => {
  it('has fixed lengths: slide durations, video lengths, repeat counts and finished passes', () => {
    const pl: Playlist = {
      id: 'L',
      name: 'L',
      items: [
        { id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } },
        { id: 'i2', kind: 'media', refId: 'v8', play: { mode: 'repeat', count: 2 } },
        { id: 'i3', kind: 'media', refId: 'img', play: { mode: 'duration', ms: 7000 } },
        { id: 'i4', kind: 'slide', refId: 'a', presentationId: 'P', play: { mode: 'duration', ms: 12_000 } },
      ],
    };
    const tl = buildTimeline(pl, source(pl));
    // a 5 + b 5 + c 12 | video 8 ×2 | image 7 | slide a until ≥ 12 s → 3 passes of 5 s
    expect(tl.steps.map((s) => s.ms)).toEqual([5000, 5000, 12_000, 8000, 8000, 7000, 5000, 5000, 5000]);
    expect(tl.cycleMs).toBe(60_000);
    // b and c share the background video: it keeps running from b's start.
    expect(tl.steps[2]!.bgStart).toBe(tl.steps[1]!.start);
    expect(tl.steps[4]!.bgStart).toBe(tl.steps[3]!.start); // same media video twice in a row
  });

  it('locates the step at any time, also across cycles and before the anchor', () => {
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    const tl = buildTimeline(pl, source(pl)); // 5 + 5 + 12 = 22 s
    const anchor = 1_000_000;
    expect(locate(tl, anchor, anchor)).toEqual({ index: 0, startAt: anchor, endAt: anchor + 5000 });
    expect(locate(tl, anchor, anchor + 7000)!.index).toBe(1);
    expect(locate(tl, anchor, anchor + 22_000 * 3 + 11_000)).toEqual({ index: 2, startAt: anchor + 66_000 + 10_000, endAt: anchor + 66_000 + 22_000 });
    expect(locate(tl, anchor, anchor - 1000)).toEqual({ index: 2, startAt: anchor - 12_000, endAt: anchor }); // clock slightly behind
  });
});

describe('synchronous playback', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('two screens started at different times show the same slide at the same time', async () => {
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    vi.setSystemTime(1_000_000);
    const anchor = 1_000_000;
    const s1 = new FakeStage();
    const seq1 = new Sequencer(s1 as unknown as Stage, () => testEnv());
    seq1.setContent(content(pl));
    seq1.play('L', anchor);
    await vi.advanceTimersByTimeAsync(7300); // the second screen starts 7.3 s later (in slide b)
    const s2 = new FakeStage();
    const seq2 = new Sequencer(s2 as unknown as Stage, () => testEnv());
    seq2.setContent(content(pl));
    seq2.play('L', anchor);
    await vi.advanceTimersByTimeAsync(60_000);
    // The late screen joins with b, its video seeked 2.3 s in; from then on both switch at
    // the same instants.
    expect(s2.shown.filter((x) => x.id !== 'CLEAR')[0]).toMatchObject({ id: 'b', startAtMs: 2300 });
    const after = (s: FakeStage) => s.shown.filter((x) => x.id !== 'CLEAR' && x.at >= 1_010_000).map((x) => `${x.id}@${x.at}`);
    expect(after(s2)).toEqual(after(s1));
    expect(after(s1).slice(0, 4)).toEqual(['c@1010000', 'a@1022000', 'b@1027000', 'c@1032000']);
  });

  it('a new anchor (e.g. a time window starting) restarts the timeline; the same one does not', async () => {
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    vi.setSystemTime(2_000_000);
    const s = new FakeStage();
    const seq = new Sequencer(s as unknown as Stage, () => testEnv(), { now: () => Date.now() + 500 }); // corrected clock
    seq.setContent(content(pl));
    seq.play('L', 2_000_000);
    await vi.advanceTimersByTimeAsync(1000);
    seq.play('L', 2_000_000); // re-evaluation with the same result: no restart
    const ids = (): string[] => s.shown.filter((x) => x.id !== 'CLEAR').map((x) => x.id);
    expect(ids()).toEqual(['a']);
    seq.play('L', 2_001_500); // window started now (corrected clock)
    await vi.advanceTimersByTimeAsync(10);
    expect(ids()).toEqual(['a', 'a']);
  });
});

describe('playlists for several screens', () => {
  const wall: Playlist = {
    id: 'W',
    name: 'Wall',
    screens: 3,
    items: [
      { id: 's1', kind: 'slide', refId: 'a', presentationId: 'P', play: { mode: 'duration', ms: 6000 }, cells: [{ kind: 'slide', refId: 'b', presentationId: 'P' }, { kind: 'media', refId: 'v8' }] },
      { id: 's2', kind: 'media', refId: 'img', play: { mode: 'duration', ms: 4000 }, cells: [null, { kind: 'media', refId: 'gone' }] },
    ],
  };

  it('every track has the same steps with fixed durations; empty cells play track 1', () => {
    const src = source(wall);
    const t1 = buildTimeline(wall, src, 1);
    const t2 = buildTimeline(wall, src, 2);
    const t3 = buildTimeline(wall, src, 3);
    for (const tl of [t1, t2, t3]) {
      expect(tl.steps.map((s) => [s.start, s.ms])).toEqual([[0, 6000], [6000, 4000]]);
      expect(tl.cycleMs).toBe(10_000);
    }
    expect(t1.steps[0]!.slide.id).toBe('a');
    expect(t2.steps[0]!.slide.id).toBe('b');
    expect(t2.steps[1]!.slide.background).toMatchObject({ type: 'image', assetId: 'img' }); // empty cell = track 1
    // A video shorter than the step loops (fixed duration); a missing medium stays black.
    expect(t3.steps[0]!.slide.duration).toEqual({ mode: 'fixed', ms: 6000 });
    expect(t3.steps[0]!.slide.background).toMatchObject({ type: 'video', assetId: 'v8' });
    expect(t3.steps[1]!.slide.background).toEqual({ type: 'color', color: '#000000' });
    // A place beyond the tracks plays track 1.
    expect(buildTimeline(wall, src, 7).steps[0]!.slide.id).toBe('a');
  });

  it('three screens switch together, each with its own track', async () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(5_000_000);
      const stages = [1, 2, 3].map((pos) => {
        const s = new FakeStage();
        const seq = new Sequencer(s as unknown as Stage, () => testEnv());
        seq.setContent(content(wall));
        seq.play('W', 5_000_000, pos);
        return s;
      });
      await vi.advanceTimersByTimeAsync(20_500);
      const at = (s: FakeStage) => s.shown.filter((x) => x.id !== 'CLEAR').map((x) => x.at);
      expect(at(stages[0]!)).toEqual([5_000_000, 5_006_000, 5_010_000, 5_016_000, 5_020_000]);
      expect(at(stages[1]!)).toEqual(at(stages[0]!));
      expect(at(stages[2]!)).toEqual(at(stages[0]!));
      expect(stages[1]!.shown.filter((x) => x.id !== 'CLEAR').map((x) => x.id).slice(0, 2)).toEqual(['b', 'media-s2']);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('build time compensation', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('a slow screen starts each step as early as it took to build last time', async () => {
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    vi.setSystemTime(3_000_000);
    class SlowStage extends FakeStage {
      visible: { id: string; at: number }[] = [];
      override async show(s: StageSlide): Promise<void> {
        vi.setSystemTime(Date.now() + 150); // building the slide takes 150 ms of main-thread time
        this.visible.push({ id: s.slide.id, at: Date.now() });
      }
    }
    const st = new SlowStage();
    const seq = new Sequencer(st as unknown as Stage, () => testEnv());
    seq.setContent(content(pl));
    seq.play('L', 3_000_000);
    await vi.advanceTimersByTimeAsync(22_000 * 2 + 1000);
    // First cycle: every step is 150 ms late (build time unknown); from the second cycle
    // on each step is visible exactly on its boundary.
    expect(st.visible.slice(0, 3).map((v) => v.at - 3_000_000)).toEqual([150, 5150, 10_150]);
    expect(st.visible.slice(3, 6).map((v) => v.at - 3_000_000)).toEqual([22_000, 27_000, 32_000]);
  });

  it('a screen that fell far behind rejoins at the current position', async () => {
    const pl: Playlist = { id: 'L', name: 'L', items: [{ id: 'i1', kind: 'presentation', refId: 'P', play: { mode: 'repeat', count: 1 } }] };
    vi.setSystemTime(4_000_000);
    const st = new FakeStage();
    const seq = new Sequencer(st as unknown as Stage, () => testEnv());
    seq.setContent(content(pl));
    seq.play('L', 4_000_000);
    await vi.advanceTimersByTimeAsync(100);
    vi.setSystemTime(4_000_000 + 16_000); // the clock jumped (e.g. a frozen tab): in slide c now
    await vi.advanceTimersByTimeAsync(5000);
    const ids = st.shown.filter((x) => x.id !== 'CLEAR').map((x) => x.id);
    expect(ids).toContain('c');
    expect(st.shown.find((x) => x.id === 'c')?.startAtMs).toBeGreaterThan(5000); // video seeked into c
  });
});
