import { describe, expect, it } from 'vitest';
import type { FontManifest } from '@opensignage/contracts';
import { fontFaceCss } from './fonts';

describe('fontFaceCss', () => {
  it('puts the content hash into the font URL, so an updated file is never taken from the cache', () => {
    const m: FontManifest = {
      version: 1,
      fallback: 'Noto Sans',
      families: [
        {
          id: 'barlow-semi-condensed',
          family: 'Barlow Semi Condensed',
          category: 'condensed',
          license: 'OFL-1.1',
          scripts: ['latin'],
          files: [
            {
              file: 'barlow-semi-condensed/BarlowSemiCondensed-Regular.woff2',
              style: 'normal',
              weight: [400, 400],
              format: 'woff2',
              sha256: '0123456789abcdef0123',
              size: 1,
            },
          ],
        },
      ],
    };
    expect(fontFaceCss(m, '/fonts/bundled')).toContain(
      'url("/fonts/bundled/barlow-semi-condensed/BarlowSemiCondensed-Regular.woff2?v=0123456789ab") format("woff2")',
    );
  });
});
