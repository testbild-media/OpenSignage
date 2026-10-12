// Synchronous playback: the whole playlist as a timeline with fixed
// durations, so every screen computes from the clock which slide is on – no messages
// between the screens, no drift: position = (now − anchor) mod cycle length.
import type { Playlist, PlaylistItem, Presentation, Slide } from '@opensignage/contracts';
import { LIMITS } from '@opensignage/contracts';
import type { MediaInfo, SequencerContent } from './sequencer';
import { backgroundSignature } from './stage';

export interface TimelineStep {
  slide: Slide;
  pres: Presentation;
  /** Start within the cycle and length, in ms. */
  start: number;
  ms: number;
  /** Start of the run of consecutive steps with the same background (a background video
   * keeps running across them): a screen that joins late seeks the video to now − bgStart. */
  bgStart: number;
}

export interface Timeline {
  steps: TimelineStep[];
  cycleMs: number;
}

/** Fallback when the length of a video is unknown (it should come from the manifest). */
const UNKNOWN_VIDEO_MS = 30_000;
const DEFAULT_IMAGE_MS = 10_000;

/** Length of one slide: fixed duration, or the background video's length ("until the video
 *  ends", capped by maxMs). */
export function slideMs(slide: Slide, media: Map<string, MediaInfo>): number {
  const d = slide.duration;
  if (d.mode === 'fixed') return Math.max(LIMITS.minSlideMs, d.ms);
  const b = slide.background;
  const len = b.type === 'video' ? media.get(b.assetId)?.durationMs : undefined;
  const cap = d.maxMs || LIMITS.defaultVideoMaxMs;
  return Math.max(LIMITS.minSlideMs, Math.min(cap, len && len > 0 ? len : Math.min(cap, UNKNOWN_VIDEO_MS)));
}

export interface TimelineSource {
  content: SequencerContent;
  /** Slides of an item (null = not playable), the same as the sequencer plays. */
  passes(item: PlaylistItem): { pres: Presentation; slides: Slide[] } | null;
}

/** Tracks of a playlist (at least 1). */
export function playlistTracks(pl: Playlist): number {
  return pl.screens && pl.screens > 1 ? pl.screens : 1;
}

/** The track a screen at `position` plays: its own, or track 1 beyond the tracks. */
export function trackFor(pl: Playlist, position: number): number {
  return position >= 1 && position <= playlistTracks(pl) ? position : 1;
}

/** The item a track plays in one step: the item itself (track 1, empty cells) or its cell. */
export function cellItem(item: PlaylistItem, track: number): PlaylistItem {
  const cell = track > 1 ? item.cells?.[track - 2] : null;
  if (!cell) return item;
  return { id: `${item.id}~${track}`, kind: cell.kind, refId: cell.refId, presentationId: cell.presentationId, fit: cell.fit, play: item.play };
}

/**
 * Builds the timeline of a playlist with the sequencer's rules: repeat = exactly N passes;
 * duration = passes until the time is reached, a started pass is finished (at least one).
 * Media items: a video plays its length per pass, an image its duration. A playlist for
 * several screens plays `track`: every step lasts exactly its duration (a slide shows that
 * long, a shorter video loops), so all tracks switch together.
 */
export function buildTimeline(pl: Playlist, src: TimelineSource, track = 1): Timeline {
  const steps: TimelineStep[] = [];
  let t = 0;
  if (playlistTracks(pl) > 1) {
    for (const item of pl.items) {
      const ms = Math.max(LIMITS.minSlideMs, item.play.mode === 'duration' ? item.play.ms : 0);
      const p = src.passes(cellItem(item, trackFor(pl, track)));
      const s = p?.slides[0];
      if (!p || !s) {
        // Not playable here: this screen stays black for the step, the others switch on time.
        const blank = blankPresentation(`blank-${item.id}`, ms);
        steps.push({ slide: blank.slides[0]!, pres: blank, start: t, ms, bgStart: t });
        t += ms;
        continue;
      }
      steps.push({ slide: { ...s, duration: { mode: 'fixed', ms } }, pres: p.pres, start: t, ms, bgStart: t });
      t += ms;
    }
    chainBackgrounds(steps);
    return { steps, cycleMs: t };
  }
  for (const item of pl.items) {
    const p = src.passes(item);
    if (!p || p.slides.length === 0) continue;
    const lens = p.slides.map((s) => itemSlideMs(item, s, src.content.media));
    const pass = lens.reduce((a, b) => a + b, 0);
    if (pass <= 0) continue;
    const play = item.play;
    const count = play.mode === 'repeat' ? Math.max(1, play.count) : play.ms > 0 ? Math.max(1, Math.ceil(play.ms / pass)) : 1;
    for (let n = 0; n < count; n++) {
      p.slides.forEach((s, i) => {
        steps.push({ slide: s, pres: p.pres, start: t, ms: lens[i]!, bgStart: t });
        t += lens[i]!;
      });
    }
  }
  chainBackgrounds(steps);
  return { steps, cycleMs: t };
}

/** A black one-slide presentation (a step a track cannot play). */
export function blankPresentation(id: string, ms: number): Presentation {
  const empty = { font: 'Montserrat', size: 24, color: '#ffffff', bold: false, italic: false, underline: false, strike: false, letterSpacing: 0, lineHeight: 1.2, align: 'left' as const };
  const slide: Slide = { id, name: '', hidden: false, duration: { mode: 'fixed', ms }, background: { type: 'color', color: '#000000' }, transition: { type: 'cut', ms: 0 }, elements: [] };
  return { id, name: '', width: 1920, height: 1080, slides: [slide], typography: { heading: empty, subheading: empty, item: empty, description: empty, price: empty } };
}

/** Runs of identical backgrounds (back to back): the stage keeps the video running from the first one. */
function chainBackgrounds(steps: TimelineStep[]): void {
  for (let i = 1; i < steps.length; i++) {
    const prev = steps[i - 1]!;
    if (backgroundSignature(steps[i]!.slide.background) === backgroundSignature(prev.slide.background)) {
      steps[i]!.bgStart = prev.bgStart;
    }
  }
}

function itemSlideMs(item: PlaylistItem, s: Slide, media: Map<string, MediaInfo>): number {
  if (item.kind === 'media') {
    const info = media.get(item.refId);
    if (info?.kind === 'video') return Math.max(LIMITS.minSlideMs, info.durationMs && info.durationMs > 0 ? info.durationMs : UNKNOWN_VIDEO_MS);
    return Math.max(LIMITS.minSlideMs, item.play.mode === 'duration' && item.play.ms > 0 ? item.play.ms : DEFAULT_IMAGE_MS);
  }
  return slideMs(s, media);
}

export interface Position {
  index: number;
  /** Absolute times (ms, same clock as `now`) of the step's start and end. */
  startAt: number;
  endAt: number;
}

/** The step that is on at `now` for a timeline started at `anchor` (both ms epoch). */
export function locate(tl: Timeline, anchor: number, now: number): Position | null {
  if (tl.cycleMs <= 0 || tl.steps.length === 0) return null;
  const elapsed = now - anchor;
  const cycle = Math.floor(elapsed / tl.cycleMs);
  const within = elapsed - cycle * tl.cycleMs; // 0 ≤ within < cycleMs, also before the anchor
  let lo = 0;
  let hi = tl.steps.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (tl.steps[mid]!.start <= within) lo = mid;
    else hi = mid - 1;
  }
  const step = tl.steps[lo]!;
  const startAt = anchor + cycle * tl.cycleMs + step.start;
  return { index: lo, startAt, endAt: startAt + step.ms };
}
