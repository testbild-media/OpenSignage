// Pure helpers for time schedule windows: parsing, midnight crossing, the weekly timeline
// layout and "now" in the configured time zone. Semantics mirror internal/schedule: days
// are ISO weekdays (1 = Monday) of the window START, end <= start crosses midnight and
// end == start means 24 hours.
import type { Window } from '@opensignage/contracts';

export const DAY_MINUTES = 24 * 60;

/** "HH:MM" → minutes since midnight, or null. */
export function parseHHMM(s: string): number | null {
  const m = /^([0-9]{2}):([0-9]{2})$/.exec(s);
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return h * 60 + min;
}

export function formatHHMM(minutes: number): string {
  const m = ((minutes % DAY_MINUTES) + DAY_MINUTES) % DAY_MINUTES;
  return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
}

/** Length of a window in minutes (end <= start wraps over midnight; equal = 24 h). */
export function windowLength(start: string, end: string): number {
  const s = parseHHMM(start);
  const e = parseHHMM(end);
  if (s === null || e === null) return 0;
  const len = e - s;
  return len <= 0 ? len + DAY_MINUTES : len;
}

export type MidnightInfo = 'same_day' | 'crosses' | 'full_day';

export function midnightInfo(start: string, end: string): MidnightInfo | null {
  const s = parseHHMM(start);
  const e = parseHHMM(end);
  if (s === null || e === null) return null;
  if (e === s) return 'full_day';
  return e < s ? 'crosses' : 'same_day';
}

export function nextDay(day: number): number {
  return day === 7 ? 1 : day + 1;
}

/** A piece of a window on one weekday row of the timeline. */
export interface Segment {
  day: number; // 1..7
  start: number; // minutes, inclusive
  end: number; // minutes, exclusive (≤ 1440)
  /** The piece continues a window that started the day before. */
  continued: boolean;
  /** The window continues on the next day. */
  continues: boolean;
}

/** Splits a weekly window into per-day segments (a window over midnight gives two). */
export function windowSegments(w: Window): Segment[] {
  const s = parseHHMM(w.start);
  const e = parseHHMM(w.end);
  if (s === null || e === null) return [];
  const out: Segment[] = [];
  for (const day of [...w.days].sort((a, b) => a - b)) {
    if (day < 1 || day > 7) continue;
    if (e > s) {
      out.push({ day, start: s, end: e, continued: false, continues: false });
    } else {
      out.push({ day, start: s, end: DAY_MINUTES, continued: false, continues: e > 0 });
      if (e > 0) out.push({ day: nextDay(day), start: 0, end: e, continued: true, continues: false });
    }
  }
  return out;
}

/** Collapses weekdays into ranges: [1,2,3,4,5,7] → [[1,5],[7,7]]. */
export function dayRanges(days: number[]): [number, number][] {
  const sorted = [...new Set(days)].filter((d) => d >= 1 && d <= 7).sort((a, b) => a - b);
  const out: [number, number][] = [];
  for (const d of sorted) {
    const last = out[out.length - 1];
    if (last && last[1] === d - 1) last[1] = d;
    else out.push([d, d]);
  }
  return out;
}

/** "Mon–Fri, Sun" with the given short labels (index 1..7). */
export function formatDays(days: number[], label: (d: number) => string): string {
  if (days.length === 7) return `${label(1)}–${label(7)}`;
  return dayRanges(days)
    .map(([a, b]) => (a === b ? label(a) : b === a + 1 ? `${label(a)}, ${label(b)}` : `${label(a)}–${label(b)}`))
    .join(', ');
}

/** Short weekday names for ISO days 1..7 in a language (2024-01-01 was a Monday). */
export function weekdayLabels(lang: string, style: 'short' | 'long' = 'short'): Record<number, string> {
  const out: Record<number, string> = {};
  let fmt: Intl.DateTimeFormat;
  try {
    fmt = new Intl.DateTimeFormat(lang, { weekday: style, timeZone: 'UTC' });
  } catch {
    fmt = new Intl.DateTimeFormat('en', { weekday: style, timeZone: 'UTC' });
  }
  for (let d = 1; d <= 7; d++) out[d] = fmt.format(new Date(Date.UTC(2024, 0, d)));
  return out;
}

/** Weekday (ISO) and minute of day of an instant in an IANA time zone. */
export function nowInTimezone(at: Date, timeZone: string): { day: number; minute: number } {
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat('en-US', { timeZone, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at);
  } catch {
    parts = new Intl.DateTimeFormat('en-US', { timeZone: 'UTC', weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(at);
  }
  const get = (type: string): string => parts.find((p) => p.type === type)?.value ?? '';
  const names = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  const day = names.indexOf(get('weekday')) + 1 || 1;
  const hour = Number(get('hour')) % 24;
  const minute = Number(get('minute'));
  return { day, minute: hour * 60 + minute };
}

/** Whether a window is active at weekday/minute (same rule as internal/schedule). */
export function windowActive(w: Window, day: number, minute: number): boolean {
  return windowSegments(w).some((s) => s.day === day && minute >= s.start && minute < s.end);
}
