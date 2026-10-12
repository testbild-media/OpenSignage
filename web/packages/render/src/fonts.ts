import type { FontManifest, ManifestFontFile } from '@opensignage/contracts';

/** Builds @font-face rules for the bundled fonts (baseUrl points to the fonts folder). */
export function fontFaceCss(manifest: FontManifest, baseUrl: string): string {
  const rules: string[] = [];
  for (const fam of manifest.families) {
    for (const f of fam.files) {
      const fmt = f.format ?? (f.file.endsWith('.woff2') ? 'woff2' : f.file.endsWith('.otf') ? 'opentype' : 'truetype');
      const weight = f.weight[0] === f.weight[1] ? `${f.weight[0]}` : `${f.weight[0]} ${f.weight[1]}`;
      const stretch = f.width ? `font-stretch:${f.width[0]}% ${f.width[1]}%;` : '';
      // The content hash in the URL: a font file changed by an update is never taken from the cache.
      const version = f.sha256 ? `?v=${f.sha256.slice(0, 12)}` : '';
      rules.push(
        `@font-face{font-family:"${fam.family}";src:url("${baseUrl}/${encodeURI(f.file)}${version}") format("${fmt}");font-weight:${weight};font-style:${f.style};${stretch}font-display:block}`,
      );
    }
  }
  return rules.join('\n');
}

/** @font-face rules for uploaded fonts of a deployment. */
export function customFontCss(fonts: { family: string; files: ManifestFontFile[] }[], urlFor: (f: ManifestFontFile) => string): string {
  return fonts
    .flatMap((fam) =>
      fam.files.map(
        (f) =>
          `@font-face{font-family:"${fam.family}";src:url("${urlFor(f)}") format("${f.format}");font-weight:${f.weight[0]} ${f.weight[1]};font-style:${f.style};font-display:block}`,
      ),
    )
    .join('\n');
}

/** Installs or replaces a style element with the given id. */
export function installStyle(id: string, css: string, doc: Document = document): void {
  let el = doc.getElementById(id) as HTMLStyleElement | null;
  if (!el) {
    el = doc.createElement('style');
    el.id = id;
    doc.head.appendChild(el);
  }
  if (el.textContent !== css) el.textContent = css;
}
