import { describe, expect, it } from 'vitest';
import {
  addDays,
  dayRanges,
  formatDays,
  inPeriod,
  isDate,
  isoWeekdayOf,
  nextOccurrence,
  periodProblem,
  periodState,
  formatHHMM,
  midnightInfo,
  nowInTimezone,
  parseHHMM,
  weekdayLabels,
  windowActive,
  windowLength,
  windowSegments,
} from './windows';

describe('parseHHMM', () => {
  it('parses valid times', () => {
    expect(parseHHMM('00:00')).toBe(0);
    expect(parseHHMM('17:30')).toBe(1050);
    expect(parseHHMM('23:59')).toBe(1439);
  });
  it('rejects invalid times', () => {
    expect(parseHHMM('24:00')).toBeNull();
    expect(parseHHMM('7:30')).toBeNull();
    expect(parseHHMM('12:60')).toBeNull();
    expect(parseHHMM('')).toBeNull();
  });
  it('formats back', () => {
    expect(formatHHMM(1050)).toBe('17:30');
    expect(formatHHMM(1440)).toBe('00:00');
  });
});

describe('midnight handling', () => {
  it('detects crossing and full days', () => {
    expect(midnightInfo('17:00', '19:00')).toBe('same_day');
    expect(midnightInfo('22:00', '02:00')).toBe('crosses');
    expect(midnightInfo('22:00', '00:00')).toBe('crosses');
    expect(midnightInfo('06:00', '06:00')).toBe('full_day');
    expect(midnightInfo('x', '06:00')).toBeNull();
  });
  it('computes lengths', () => {
    expect(windowLength('17:00', '19:00')).toBe(120);
    expect(windowLength('22:00', '02:00')).toBe(240);
    expect(windowLength('06:00', '06:00')).toBe(1440);
  });
});

describe('windowSegments', () => {
  it('keeps same-day windows on their day', () => {
    expect(windowSegments({ days: [1, 5], start: '17:00', end: '19:00' })).toEqual([
      { day: 1, start: 1020, end: 1140, continued: false, continues: false },
      { day: 5, start: 1020, end: 1140, continued: false, continues: false },
    ]);
  });

  it('splits windows over midnight, wrapping Sunday to Monday', () => {
    expect(windowSegments({ days: [7], start: '22:00', end: '02:00' })).toEqual([
      { day: 7, start: 1320, end: 1440, continued: false, continues: true },
      { day: 1, start: 0, end: 120, continued: true, continues: false },
    ]);
  });

  it('ends exactly at midnight without an empty piece', () => {
    expect(windowSegments({ days: [3], start: '20:00', end: '00:00' })).toEqual([{ day: 3, start: 1200, end: 1440, continued: false, continues: false }]);
  });

  it('treats equal start and end as 24 hours', () => {
    const segs = windowSegments({ days: [2], start: '06:00', end: '06:00' });
    expect(segs).toHaveLength(2);
    expect(segs.reduce((n, s) => n + s.end - s.start, 0)).toBe(1440);
    expect(windowSegments({ days: [2], start: '00:00', end: '00:00' })).toEqual([{ day: 2, start: 0, end: 1440, continued: false, continues: false }]);
  });

  it('agrees with windowActive for a midnight window', () => {
    const w = { days: [5], start: '22:00', end: '03:00' };
    expect(windowActive(w, 5, 23 * 60)).toBe(true);
    expect(windowActive(w, 6, 2 * 60)).toBe(true);
    expect(windowActive(w, 6, 3 * 60)).toBe(false);
    expect(windowActive(w, 5, 2 * 60)).toBe(false);
  });
});

describe('days', () => {
  it('collapses ranges', () => {
    expect(dayRanges([7, 1, 2, 3, 4, 5])).toEqual([
      [1, 5],
      [7, 7],
    ]);
    expect(dayRanges([])).toEqual([]);
  });
  it('formats with labels', () => {
    const label = (d: number): string => ['', 'Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'][d] ?? '';
    expect(formatDays([1, 2, 3, 4, 5], label)).toBe('Mo–Fr');
    expect(formatDays([6, 7], label)).toBe('Sa, Su');
    expect(formatDays([1, 3, 5], label)).toBe('Mo, We, Fr');
    expect(formatDays([1, 2, 3, 4, 5, 6, 7], label)).toBe('Mo–Su');
  });
  it('builds localized weekday labels starting on Monday', () => {
    expect(weekdayLabels('en')[1]).toBe('Mon');
    expect(weekdayLabels('en')[7]).toBe('Sun');
    expect(weekdayLabels('de')[1]).toMatch(/^Mo/);
  });
});

describe('nowInTimezone', () => {
  it('converts an instant to weekday and minute in the zone', () => {
    // 2026-10-11 is a Sunday; 22:30 UTC is 00:30 on Monday in Berlin (CEST, UTC+2).
    const at = new Date(Date.UTC(2026, 9, 11, 22, 30));
    expect(nowInTimezone(at, 'UTC')).toEqual({ day: 7, minute: 22 * 60 + 30, date: '2026-10-11' });
    expect(nowInTimezone(at, 'Europe/Berlin')).toEqual({ day: 1, minute: 30, date: '2026-10-12' });
  });
});

describe('periods of dates', () => {
  // 2026-11-07 is a Saturday.
  const hh = { days: [6], start: '17:00', end: '19:00', from: '2026-11-07', until: '2026-11-07' };

  it('does calendar arithmetic', () => {
    expect(addDays('2026-11-07', 1)).toBe('2026-11-08');
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28');
    expect(addDays('2026-10-25', 1)).toBe('2026-10-26'); // daylight saving ends: still one day
    expect(isoWeekdayOf('2026-11-07')).toBe(6);
    expect(isoWeekdayOf('2026-11-08')).toBe(7);
    expect(isDate('2026-11-07')).toBe(true);
    expect(isDate('2026-02-30')).toBe(false);
    expect(isDate('7.11.2026')).toBe(false);
  });

  it('checks a period like the server', () => {
    expect(inPeriod(hh, '2026-11-07')).toBe(true);
    expect(inPeriod(hh, '2026-11-14')).toBe(false);
    expect(inPeriod({ ...hh, from: undefined }, '2020-01-04')).toBe(true);
    expect(periodProblem([6], '2026-11-07', '2026-11-07')).toBe(null);
    expect(periodProblem([6], '2026-11-08', '2026-11-07')).toBe('order');
    expect(periodProblem([1], '2026-11-07', '2026-11-08')).toBe('days');
    expect(periodProblem([1], '2026-11-07', '2026-11-13')).toBe(null);
    expect(periodProblem([1], '2026-11-07', '')).toBe(null);
  });

  it('finds the next occurrence and the state', () => {
    expect(nextOccurrence(hh, '2026-10-19')).toBe('2026-11-07');
    expect(nextOccurrence(hh, '2026-11-08')).toBe(null);
    expect(nextOccurrence({ days: [6], start: '17:00', end: '19:00', from: '2026-10-01' }, '2026-10-19')).toBe('2026-10-24');
    expect(periodState(hh, '2026-10-19')).toBe('upcoming');
    expect(periodState(hh, '2026-11-07')).toBe('current');
    expect(periodState(hh, '2026-11-08')).toBe('expired');
    expect(periodState({ days: [6], start: '17:00', end: '19:00' }, '2026-11-08')).toBe(null);
  });
});
