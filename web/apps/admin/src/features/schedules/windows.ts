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

/** Weekday (ISO), minute of day and date (YYYY-MM-DD) of an instant in an IANA time zone. */
export function nowInTimezone(at: Date, timeZone: string): { day: number; minute: number; date: string } {
  const opts: Intl.DateTimeFormatOptions = { weekday: 'short', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' };
  let parts: Intl.DateTimeFormatPart[];
  try {
    parts = new Intl.DateTimeFormat('en-US', { ...opts, timeZone }).formatToParts(at);
  } catch {
    parts = new Intl.DateTimeFormat('en-US', { ...opts, timeZone: 'UTC' }).formatToParts(at);
  }
  const get = (type: string): string => parts.find((p) => p.type === type)?.value ?? '';
  const names = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  const day = names.indexOf(get('weekday')) + 1 || 1;
  const hour = Number(get('hour')) % 24;
  const minute = Number(get('minute'));
  return { day, minute: hour * 60 + minute, date: `${get('year')}-${get('month')}-${get('day')}` };
}

// ---- periods of dates (YYYY-MM-DD; calendar arithmetic in UTC, no time zone involved)

const DAY_MS = 86_400_000;

/** The date n days after (or before) a date. */
export function addDays(date: string, n: number): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) + n * DAY_MS).toISOString().slice(0, 10);
}

/** ISO weekday (1 = Monday) of a date. */
export function isoWeekdayOf(date: string): number {
  const d = new Date(Date.parse(`${date}T00:00:00Z`)).getUTCDay();
  return d === 0 ? 7 : d;
}

/** Whether s is a real date "YYYY-MM-DD" (not 2026-02-30). */
export function isDate(s: string): boolean {
  return /^\d{4}-\d{2}-\d{2}$/.test(s) && !Number.isNaN(Date.parse(`${s}T00:00:00Z`)) && addDays(s, 0) === s;
}

export function hasPeriod(w: Window): boolean {
  return !!(w.from || w.until);
}

/** Whether an occurrence starting on the date lies in the window's period (internal/schedule.InPeriod). */
export function inPeriod(w: Window, date: string): boolean {
  return (!w.from || date >= w.from) && (!w.until || date <= w.until);
}

/** What is wrong with a period (same rules as the server): the end before the start, or none
 *  of the weekdays inside a period shorter than a week. */
export function periodProblem(days: number[], from: string, until: string): 'order' | 'days' | null {
  if (!from || !until) return null;
  if (until < from) return 'order';
  if ((Date.parse(`${until}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / DAY_MS < 6) {
    for (let d = from; d <= until; d = addDays(d, 1)) if (days.includes(isoWeekdayOf(d))) return null;
    return 'days';
  }
  return null;
}

/** The first day from today on which the window starts within its period (null: none left). */
export function nextOccurrence(w: Window, today: string): string | null {
  let d = w.from && w.from > today ? w.from : today;
  for (let i = 0; i < 7; i++, d = addDays(d, 1)) {
    if (w.until && d > w.until) return null;
    if (w.days.includes(isoWeekdayOf(d))) return d;
  }
  return null;
}

/** Where today lies relative to the window's period (null: no period). */
export function periodState(w: Window, today: string): 'expired' | 'upcoming' | 'current' | null {
  if (!hasPeriod(w)) return null;
  if (w.until && w.until < today) return 'expired';
  if (w.from && w.from > today) return 'upcoming';
  return 'current';
}

/** A date as the language writes it ("7.11.2026"). */
export function formatDate(date: string, lang: string, withYear = true): string {
  const opts: Intl.DateTimeFormatOptions = withYear ? { day: 'numeric', month: 'numeric', year: 'numeric', timeZone: 'UTC' } : { day: 'numeric', month: 'numeric', timeZone: 'UTC' };
  try {
    return new Intl.DateTimeFormat(lang, opts).format(new Date(`${date}T00:00:00Z`));
  } catch {
    return date;
  }
}

/** Whether a window is active at weekday/minute (same rule as internal/schedule). */
export function windowActive(w: Window, day: number, minute: number): boolean {
  return windowSegments(w).some((s) => s.day === day && minute >= s.start && minute < s.end);
}
