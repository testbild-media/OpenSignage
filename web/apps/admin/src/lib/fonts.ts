import type { FontManifest, ManifestFontFile } from '@opensignage/contracts';
import { customFontCss, fontFaceCss, installStyle } from '@opensignage/render';
import { get } from './api';

let manifest: Promise<FontManifest | null> | null = null;
let custom: Promise<string[]> | null = null;

/** Bundled fonts served by the server under /fonts (offline, never from a CDN). */
export function loadFontManifest(): Promise<FontManifest | null> {
  // Revalidated: an update can add families (an older copy may still sit in the cache).
  manifest ??= fetch('/fonts/manifest.json', { cache: 'no-cache' })
    .then((r) => (r.ok ? (r.json() as Promise<FontManifest>) : null))
    .catch(() => null);
  return manifest;
}

/**
 * Order of every font list (editor, fonts page, ticker templates): by name only – bundled
 * and uploaded fonts mixed, upper/lower case and accents ignored, numbers in numeric order.
 */
export function byFamily(a: string, b: string): number {
  return (
    a.localeCompare(b, undefined, { sensitivity: 'base', numeric: true }) || (a < b ? -1 : a > b ? 1 : 0)
  );
}

type CustomFace = {
  id: string;
  family: string;
  style: string;
  weightMin: number;
  weightMax: number;
  format?: string;
  url?: string;
};

/**
 * Uploaded fonts: installs their @font-face rules (editor, ticker templates and previews) and
 * returns the family names, empty when they cannot be loaded. `fresh` loads them again, so a
 * page with a font picker offers a font uploaded in the meantime.
 */
export function loadCustomFonts(fresh = false): Promise<string[]> {
  if (custom && !fresh) return custom;
  const p = get<{ fonts: { family: string; source: 'bundled' | 'custom'; faces?: CustomFace[] }[] }>(
    '/api/v1/fonts',
  )
    .then((r) => {
      const families = new Map<string, { family: string; files: ManifestFontFile[] }>();
      for (const f of r.fonts.filter((x) => x.source === 'custom').flatMap((x) => x.faces ?? [])) {
        const fam = families.get(f.family) ?? { family: f.family, files: [] };
        fam.files.push({
          url: f.url ?? `/fonts/custom/${f.id}`,
          sha256: '',
          size: 0,
          weight: [f.weightMin, f.weightMax],
          style: f.style === 'italic' ? 'italic' : 'normal',
          format: f.format ?? 'woff2',
        });
        families.set(f.family, fam);
      }
      installStyle(
        'os-custom-fonts',
        customFontCss([...families.values()], (f) => f.url),
      );
      return [...families.keys()];
    })
    .catch(() => {
      if (custom === p) custom = null; // try again next time
      return [];
    });
  custom = p;
  return p;
}

/** Installs @font-face rules for all bundled fonts (UI uses Montserrat; editor previews all). */
export function installUiFonts(): void {
  void loadFontManifest().then((m) => {
    if (m) installStyle('os-fonts', fontFaceCss(m, '/fonts'));
  });
}
