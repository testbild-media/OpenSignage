import { describe, expect, it } from 'vitest';
import { groupPhases, phaseTime, type Phase, type Version } from './versions';

const v = (version: number, phase: number, at: string, label = ''): Version => ({
  version,
  phase,
  publishedAt: at,
  publishedBy: 'admin',
  slideCount: 3,
  label,
});

describe('version history', () => {
  const versions = [
    v(9, 7, '2026-10-17T14:51:00Z'),
    v(8, 7, '2026-10-17T14:20:00Z', 'Summer menu'),
    v(7, 7, '2026-10-17T14:02:00Z'),
    v(6, 6, '2026-10-10T20:00:00Z'),
  ];

  it('groups versions into phases, newest first', () => {
    const ps = groupPhases(versions);
    expect(ps.map((p) => [p.id, p.versions.map((x) => x.version)])).toEqual([
      [7, [9, 8, 7]],
      [6, [6]],
    ]);
    expect(groupPhases([])).toEqual([]);
  });

  it('shows the time span of a phase', () => {
    const fmt = new Intl.DateTimeFormat('de', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' });
    const [long, single] = groupPhases(versions) as [Phase, Phase];
    // one date, both times (the exact punctuation is up to ICU)
    const span = phaseTime(long, fmt);
    expect(span).toBe(fmt.formatRange(new Date('2026-10-17T14:02:00Z'), new Date('2026-10-17T14:51:00Z')));
    expect(span.split('17.10.2026').length).toBe(2);
    expect(span).toContain('14:02');
    expect(span).toContain('14:51');
    expect(phaseTime(single, fmt)).toBe(fmt.format(new Date('2026-10-10T20:00:00Z')));
  });
});
