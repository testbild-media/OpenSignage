import { afterEach, describe, expect, it, vi } from 'vitest';
import { byFamily, loadCustomFonts } from './fonts';

describe('byFamily', () => {
  it('sorts font names alphabetically, case and accents ignored, numbers numerically', () => {
    const names = [
      'Source Serif 4',
      'abril',
      'Bebas Neue',
      'Source Sans 3',
      'Énergie',
      'Anton',
      'Font 10',
      'Font 9',
      'DM Sans',
      'Barlow',
    ];
    expect([...names].sort(byFamily)).toEqual([
      'abril',
      'Anton',
      'Barlow',
      'Bebas Neue',
      'DM Sans',
      'Énergie',
      'Font 9',
      'Font 10',
      'Source Sans 3',
      'Source Serif 4',
    ]);
  });
});

describe('loadCustomFonts', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns the uploaded families and installs their @font-face rules (ticker templates, editor)', async () => {
    const fetchMock = vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            fonts: [
              { family: 'Montserrat', source: 'bundled' },
              {
                family: 'Bar Script',
                source: 'custom',
                faces: [
                  {
                    id: 'f1',
                    family: 'Bar Script',
                    style: 'normal',
                    weightMin: 400,
                    weightMax: 700,
                    format: 'woff2',
                    url: '/fonts/custom/f1',
                  },
                  {
                    id: 'f2',
                    family: 'Bar Script',
                    style: 'italic',
                    weightMin: 400,
                    weightMax: 400,
                    format: 'woff2',
                  },
                ],
              },
            ],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
    );
    vi.stubGlobal('fetch', fetchMock);
    expect(await loadCustomFonts(true)).toEqual(['Bar Script']);
    const css = document.getElementById('os-custom-fonts')?.textContent ?? '';
    expect(css).toContain('font-family:"Bar Script";src:url("/fonts/custom/f1")');
    expect(css).toContain('src:url("/fonts/custom/f2")');
    expect(css).toContain('font-style:italic');
    await loadCustomFonts(); // cached
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await loadCustomFonts(true); // a page with a font picker loads them again
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('offers no uploaded fonts when they cannot be loaded, and tries again next time', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('', { status: 500 })),
    );
    expect(await loadCustomFonts(true)).toEqual([]);
    const ok = vi.fn(async () => new Response(JSON.stringify({ fonts: [] }), { status: 200 }));
    vi.stubGlobal('fetch', ok);
    expect(await loadCustomFonts()).toEqual([]);
    expect(ok).toHaveBeenCalledTimes(1);
  });
});
