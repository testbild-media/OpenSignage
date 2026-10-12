// @vitest-environment node
import { describe, expect, it } from 'vitest';
import { ApiError } from '../../lib/api';
import {
  failureText,
  formatBytes,
  groupUsage,
  isPickable,
  isUsable,
  listingPath,
  mediaErrorText,
} from './model';
import { guessFromFilename } from '../system/Fonts';

describe('listingPath', () => {
  it('always names the top level explicitly ("root" never means "no filter")', () => {
    expect(listingPath({ folder: 'root' })).toBe('/api/v1/media?folder=root');
    expect(listingPath({ folder: '' })).toBe('/api/v1/media?folder=root');
    expect(listingPath({ folder: 'f1', kind: 'image', limit: 1 })).toBe(
      '/api/v1/media?folder=f1&kind=image&limit=1',
    );
  });

  it('searches all folders (no folder parameter) when a term is given', () => {
    expect(listingPath({ folder: 'f1', q: '  logo ' })).toBe('/api/v1/media?q=logo');
  });
});

describe('formatting and errors', () => {
  it('formats bytes', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(20 * 1024 * 1024)).toBe('20 MB');
  });

  it('shows disk_full with readable sizes', () => {
    const text = mediaErrorText(
      new ApiError(507, 'disk_full', 'x', { needed: 3 * 1024 ** 3, free: 512 * 1024 ** 2 }),
    );
    expect(text).toContain('3 GB');
    expect(text).toContain('512 MB');
  });

  it('translates stored processing failures', () => {
    expect(failureText('ffmpeg_failed: exit status 1').detail).toBe('exit status 1');
    expect(failureText('ffmpeg_failed: exit status 1').text).not.toBe(failureText('weird').text);
    expect(failureText(undefined).detail).toBe('');
  });

  it('groups draft and published usage of the same owner', () => {
    const g = groupUsage([
      { ownerType: 'presentation_draft', ownerId: 'p1', ownerName: 'Menu' },
      { ownerType: 'presentation_published', ownerId: 'p1', ownerName: 'Menu' },
      { ownerType: 'product', ownerId: 'x', ownerName: 'Beer' },
    ]);
    expect(g).toEqual([
      { type: 'presentation', id: 'p1', name: 'Menu', states: ['draft', 'published'] },
      { type: 'product', id: 'x', name: 'Beer', states: [] },
    ]);
  });
});

describe('guessFromFilename', () => {
  it('suggests family, style and weight', () => {
    expect(guessFromFilename('OpenSans-SemiBoldItalic.ttf')).toEqual({
      family: 'Open Sans',
      style: 'italic',
      weightMin: 600,
      weightMax: 600,
    });
    expect(guessFromFilename('Inter[wght].woff2')).toMatchObject({ weightMin: 100, weightMax: 900 });
    expect(guessFromFilename('Lobster-Regular.otf')).toEqual({
      family: 'Lobster',
      style: 'normal',
      weightMin: 400,
      weightMax: 400,
    });
  });
});

describe('pickability', () => {
  it('allows a processed asset and one with an earlier ready revision', () => {
    expect(isPickable({ status: 'ready', readyRevision: 1 })).toBe(true);
    expect(isPickable({ status: 'processing', readyRevision: 1 })).toBe(true);
    expect(isPickable({ status: 'failed', readyRevision: 2 })).toBe(true);
    expect(isPickable({ status: 'processing', readyRevision: 0 })).toBe(false);
    expect(isPickable({ status: 'failed' })).toBe(false);
  });

  it('follows the server rule for references', () => {
    expect(isUsable({ status: 'processing', readyRevision: 0 })).toBe(true);
    expect(isUsable({ status: 'failed', readyRevision: 0 })).toBe(false);
    expect(isUsable({ status: 'failed', readyRevision: 3 })).toBe(true);
  });
});
