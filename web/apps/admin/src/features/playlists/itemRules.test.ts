import { describe, expect, it } from 'vitest';
import type { PlaylistItem } from '@opensignage/contracts';
import { allowedModes, clampCount, defaultPlay, fmtDuration, normalizeItem, repeatHints, repeatTotal, switchMode, toStep } from './itemRules';

const defaultsDuration = { mode: 'duration' as const, durationMs: 10000, repeat: 3 };
const defaultsRepeat = { mode: 'repeat' as const, durationMs: 10000, repeat: 3 };

const item = (kind: PlaylistItem['kind'], play: PlaylistItem['play']): PlaylistItem => ({
  id: 'i1',
  kind,
  refId: 'r1',
  ...(kind === 'slide' ? { presentationId: 'p1' } : {}),
  play,
});

describe('playlist item rules', () => {
  it('single slides only repeat, images only have a duration, the rest offers both', () => {
    expect(allowedModes('slide')).toEqual(['repeat']);
    expect(allowedModes('media', 'image')).toEqual(['duration']);
    expect(allowedModes('media', 'video')).toEqual(['duration', 'repeat']);
    expect(allowedModes('presentation')).toEqual(['duration', 'repeat']);
    // media whose kind is not known yet is not restricted
    expect(allowedModes('media')).toEqual(['duration', 'repeat']);
  });

  it('new single slides play once, whatever the defaults say', () => {
    expect(defaultPlay('slide', undefined, defaultsDuration)).toEqual({ mode: 'repeat', count: 1 });
    expect(defaultPlay('slide', undefined, defaultsRepeat)).toEqual({ mode: 'repeat', count: 1 });
  });

  it('other new entries follow the defaults; images always get a duration', () => {
    expect(defaultPlay('presentation', undefined, defaultsDuration)).toEqual({ mode: 'duration', ms: 10000 });
    expect(defaultPlay('presentation', undefined, defaultsRepeat)).toEqual({ mode: 'repeat', count: 3 });
    expect(defaultPlay('media', 'video', defaultsRepeat)).toEqual({ mode: 'repeat', count: 3 });
    expect(defaultPlay('media', 'image', defaultsRepeat)).toEqual({ mode: 'duration', ms: 10000 });
  });

  it('converts a single slide with a duration (older playlists) to one repetition when edited', () => {
    const legacy = item('slide', { mode: 'duration', ms: 30000 });
    const r = normalizeItem(legacy, undefined, 10000);
    expect(r.converted).toBe(true);
    expect(r.item.play).toEqual({ mode: 'repeat', count: 1 });
    expect(r.item.presentationId).toBe('p1');
    expect(legacy.play).toEqual({ mode: 'duration', ms: 30000 }); // input untouched
  });

  it('keeps entries that follow the rules', () => {
    const slide = item('slide', { mode: 'repeat', count: 4 });
    expect(normalizeItem(slide, undefined, 10000)).toEqual({ item: slide, converted: false });
    const pres = item('presentation', { mode: 'duration', ms: 60000 });
    expect(normalizeItem(pres, undefined, 10000).converted).toBe(false);
    const video = item('media', { mode: 'repeat', count: 2 });
    expect(normalizeItem(video, 'video', 10000).converted).toBe(false);
  });

  it('gives an image with repetitions the default duration', () => {
    const r = normalizeItem(item('media', { mode: 'repeat', count: 2 }), 'image', 8000);
    expect(r).toEqual({ item: item('media', { mode: 'duration', ms: 8000 }), converted: true });
  });

  it('switching the mode starts at one repetition or the default duration', () => {
    expect(switchMode('repeat', 10000)).toEqual({ mode: 'repeat', count: 1 });
    expect(switchMode('duration', 7000)).toEqual({ mode: 'duration', ms: 7000 });
  });

  it('clamps repetitions to the server limits', () => {
    expect(clampCount(0)).toBe(1);
    expect(clampCount(Number.NaN)).toBe(1);
    expect(clampCount(2.4)).toBe(2);
    expect(clampCount(5000)).toBe(1000);
  });

  it('computes the total of repetitions', () => {
    expect(repeatTotal({ ms: 8000, exact: true }, 3)).toEqual({ ms: 24000, exact: true });
    expect(repeatTotal({ ms: 0, exact: false }, 3)).toBeNull();
    expect(repeatTotal(undefined, 3)).toBeNull();
  });

  it('names the slide duration and the total for a single slide', () => {
    expect(repeatHints('slide', { ms: 8000, exact: true }, 2)).toEqual([
      { key: 'playlists.slideOwnDuration', params: { time: '0:08' } },
      { key: 'playlists.total', params: { time: '0:16' } },
    ]);
    // "until the video ends" slides: upper bound
    expect(repeatHints('slide', { ms: 90000, exact: false }, 1)).toEqual([
      { key: 'playlists.slideOwnDurationVideo', params: { time: '1:30' } },
      { key: 'playlists.totalMax', params: { time: '1:30' } },
    ]);
    // not saved yet / slide missing: no numbers
    expect(repeatHints('slide', undefined, 2)).toEqual([{ key: 'playlists.slideOwnDurationUnknown' }]);
  });

  it('shows the total for repeated presentations and videos when the pass is known', () => {
    expect(repeatHints('presentation', { ms: 20000, exact: true }, 3)).toEqual([{ key: 'playlists.repeatHint' }, { key: 'playlists.total', params: { time: '1:00' } }]);
    expect(repeatHints('media', undefined, 3)).toEqual([{ key: 'playlists.repeatHint' }]);
  });

  it('formats durations', () => {
    expect(fmtDuration(65000)).toBe('1:05');
    expect(fmtDuration(3723000)).toBe('1:02:03');
  });
});

describe('playlists for several screens', () => {
  it('steps have a fixed duration: repetitions become the time they take', () => {
    expect(allowedModes('slide', undefined, 3)).toEqual(['duration']);
    expect(allowedModes('media', 'video', 2)).toEqual(['duration']);
    expect(defaultPlay('slide', undefined, { mode: 'repeat', durationMs: 8000, repeat: 2 }, 2)).toEqual({ mode: 'duration', ms: 8000 });
    const slide = { id: 'a', kind: 'slide' as const, refId: 's', presentationId: 'p', play: { mode: 'repeat' as const, count: 3 } };
    expect(toStep(slide, { ms: 5000, exact: true }, 10_000).play).toEqual({ mode: 'duration', ms: 15_000 });
    expect(toStep(slide, undefined, 10_000).play).toEqual({ mode: 'duration', ms: 10_000 });
    expect(normalizeItem(slide, undefined, 7000, 2)).toEqual({ item: { ...slide, play: { mode: 'duration', ms: 7000 } }, converted: true });
  });
});
