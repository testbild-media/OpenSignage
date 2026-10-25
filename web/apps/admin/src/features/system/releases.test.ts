import { describe, expect, it } from 'vitest';
import { visibleReleases, type ReleaseLike } from './releases';

describe('releases on the updates page', () => {
  const r = (version: string, state: ReleaseLike['state'], newer = false): ReleaseLike => ({ version, state, newer });

  it('lists releases on the server and newer ones, not old ones only on GitHub', () => {
    const list = [
      r('1.4.8', 'available', true),
      r('1.4.7', 'ready'),
      r('1.4.6', 'failed'),
      r('1.4.5', 'available'),
      r('1.0.0', 'available'),
      r('1.3.0', 'downloading'),
    ];
    expect(visibleReleases(list).map((x) => x.version)).toEqual(['1.4.8', '1.4.7', '1.4.6', '1.3.0']);
    // A download that just started shows while it runs.
    expect(visibleReleases(list, '1.4.5').map((x) => x.version)).toContain('1.4.5');
    expect(visibleReleases([])).toEqual([]);
  });
});
