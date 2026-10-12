// Play rules of playlist entries: every entry has either a duration or
// repetitions, never both. Images only have a duration; a single slide plays with its own
// slide duration, so it only has repetitions; presentations and videos offer both.
import type { PlayMode, PlaylistItem } from '@opensignage/contracts';

export type PlayModeName = PlayMode['mode'];

/** Limits of the server (internal/contracts: MinPlayMS, MaxPlayMS, MaxRepeat). */
export const MIN_PLAY_MS = 1000;
export const MAX_PLAY_MS = 24 * 60 * 60 * 1000;
export const MAX_REPEAT = 1000;

export interface PlayDefaults {
  mode: PlayModeName;
  durationMs: number;
  repeat: number;
}

/**
 * Modes the item dialog offers; the first one is the fallback when the entry has another.
 * A playlist for several screens (tracks > 1) has steps with a fixed duration.
 */
export function allowedModes(kind: PlaylistItem['kind'], mediaKind?: string, tracks = 1): PlayModeName[] {
  if (tracks > 1) return ['duration'];
  if (kind === 'slide') return ['repeat'];
  if (kind === 'media' && mediaKind === 'image') return ['duration'];
  return ['duration', 'repeat'];
}

/** Play mode of a new entry: the defaults from the settings, single slides once. */
export function defaultPlay(kind: PlaylistItem['kind'], mediaKind: string | undefined, d: PlayDefaults, tracks = 1): PlayMode {
  if (tracks > 1) return { mode: 'duration', ms: clampMs(d.durationMs) };
  if (kind === 'slide') return { mode: 'repeat', count: 1 };
  if (d.mode === 'repeat' && allowedModes(kind, mediaKind).includes('repeat')) return { mode: 'repeat', count: clampCount(d.repeat) };
  return { mode: 'duration', ms: clampMs(d.durationMs) };
}

/**
 * Brings an entry in line with the rules when it is edited. Older playlists may hold a
 * single slide with a duration (the server still accepts and plays it); editing turns it
 * into one repetition of the slide's own duration. An image with repetitions gets the
 * default duration.
 */
export function normalizeItem(item: PlaylistItem, mediaKind: string | undefined, fallbackMs: number, tracks = 1): { item: PlaylistItem; converted: boolean } {
  const modes = allowedModes(item.kind, mediaKind, tracks);
  if (modes.includes(item.play.mode)) return { item, converted: false };
  const play: PlayMode = modes[0] === 'repeat' ? { mode: 'repeat', count: 1 } : { mode: 'duration', ms: clampMs(fallbackMs) };
  return { item: { ...item, play }, converted: true };
}

/** Switching the mode in the dialog: repetitions start at 1, a duration at the default. */
export function switchMode(mode: PlayModeName, defaultMs: number): PlayMode {
  return mode === 'repeat' ? { mode: 'repeat', count: 1 } : { mode: 'duration', ms: clampMs(defaultMs) };
}

export function clampCount(n: number): number {
  if (!Number.isFinite(n)) return 1;
  return Math.min(MAX_REPEAT, Math.max(1, Math.round(n)));
}

export function clampMs(ms: number): number {
  if (!Number.isFinite(ms)) return MIN_PLAY_MS;
  return Math.min(MAX_PLAY_MS, Math.max(MIN_PLAY_MS, Math.round(ms)));
}

/** Length of one pass as the server estimated it (ms 0 = unknown). */
export interface ItemPass {
  ms: number;
  exact: boolean;
}

/**
 * An entry of a playlist that gets several tracks: repetitions become the duration they
 * take (or the default when unknown), so the running time stays the same.
 */
export function toStep(item: PlaylistItem, pass: ItemPass | undefined, defaultMs: number): PlaylistItem {
  if (item.play.mode === 'duration') return item;
  return { ...item, play: { mode: 'duration', ms: clampMs(repeatTotal(pass, item.play.count)?.ms ?? defaultMs) } };
}

/** Total of an entry with repetitions; null when the length of one pass is unknown. */
export function repeatTotal(pass: ItemPass | undefined, count: number): { ms: number; exact: boolean } | null {
  if (!pass || pass.ms <= 0) return null;
  return { ms: pass.ms * clampCount(count), exact: pass.exact };
}

export interface Hint {
  key: string;
  params?: Record<string, string>;
}

/**
 * Hint under the repetitions: a single slide names its own slide duration ("until the video
 * ends" slides: the upper bound), every entry with a known pass length shows the total.
 */
export function repeatHints(kind: PlaylistItem['kind'], pass: ItemPass | undefined, count: number): Hint[] {
  const out: Hint[] = [];
  if (kind === 'slide') {
    if (!pass || pass.ms <= 0) return [{ key: 'playlists.slideOwnDurationUnknown' }];
    out.push({ key: pass.exact ? 'playlists.slideOwnDuration' : 'playlists.slideOwnDurationVideo', params: { time: fmtDuration(pass.ms) } });
  } else {
    out.push({ key: 'playlists.repeatHint' });
  }
  const total = repeatTotal(pass, count);
  if (total) out.push({ key: total.exact ? 'playlists.total' : 'playlists.totalMax', params: { time: fmtDuration(total.ms) } });
  return out;
}

/** "m:ss" or "h:mm:ss". */
export function fmtDuration(ms: number): string {
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  return h ? `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}` : `${m}:${String(sec).padStart(2, '0')}`;
}
