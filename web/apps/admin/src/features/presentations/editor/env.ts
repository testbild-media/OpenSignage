// Render environment of the editor: assets from the server, live product data with the
// global status (what the screens show without zone overrides), bundled + custom fonts.
import { useEffect, useMemo, useState } from 'react';
import type { FontManifest, LiveState, Presentation, Typography } from '@opensignage/contracts';
import { LiveStore, type AssetResolver, type RenderEnv } from '@opensignage/render';
import { getLanguage } from '@opensignage/i18n';
import { get } from '../../../lib/api';
import { byFamily, loadCustomFonts, loadFontManifest } from '../../../lib/fonts';
import { useEvents } from '../../../lib/events';
import { useSession } from '../../../lib/session';

export const editorAssets: AssetResolver = {
  url(id, purpose) {
    if (!id) return null;
    const variant = purpose === 'display' ? 'display' : purpose;
    return `/api/v1/media/${encodeURIComponent(id)}/file?variant=${variant}`;
  },
};

interface ProductRow {
  id: string;
  name: string;
  description: string;
  categoryId: string;
  type: 'drink' | 'food' | 'other';
  quantity?: { value: string; unit: string };
  price: { amount: number; currency: string };
  status: 'AVAILABLE' | 'UNAVAILABLE' | 'SOLD_OUT';
  imageAssetId?: string;
  showImageInsteadOfName?: boolean;
  imageScale?: number;
  sort: number;
}

interface CategoryRow {
  id: string;
  parentId?: string | null;
  name: string;
  sort: number;
}

/** Live store for previews fed from the product API and refreshed on product events. */
export function useEditorLive(): LiveStore {
  const live = useMemo(() => new LiveStore(), []);
  const { server } = useSession();
  const [tick, setTick] = useState(0);
  useEvents(['products', 'live'], () => setTick((x) => x + 1));
  useEffect(() => {
    const ctrl = new AbortController();
    live.setLanguage(getLanguage());
    live.setClock(0, server?.timezone ?? 'UTC');
    Promise.all([
      get<{ products: ProductRow[] }>('/api/v1/products?archived=0', ctrl.signal),
      get<{ categories: CategoryRow[] }>('/api/v1/categories', ctrl.signal),
    ]).then(
      ([p, c]) => {
        const state: LiveState = {
          zone: { id: '', name: '', state: 'OPEN' },
          ticker: { text: '', visible: false, template: { font: 'Montserrat', size: 40, color: '#ffffff', background: '#201c18', speed: 120, direction: 'left', height: 64, position: 'bottom' } },
          categories: c.categories.map((x) => ({ id: x.id, parentId: x.parentId ?? undefined, name: x.name, sort: x.sort })),
          products: p.products.map((x) => ({
            id: x.id,
            name: x.name,
            description: x.description,
            categoryId: x.categoryId,
            type: x.type,
            quantity: x.quantity && x.quantity.value ? x.quantity : undefined,
            price: x.price,
            status: x.status,
            imageAssetId: x.imageAssetId || undefined,
            showImageInsteadOfName: !!x.showImageInsteadOfName,
            imageScale: x.imageScale ?? 100,
            sort: x.sort,
          })),
          serverTime: new Date().toISOString(),
        };
        live.set(state);
      },
      (e: Error) => {
        if (e.name !== 'AbortError') console.warn('preview products unavailable', e);
      },
    );
    return () => ctrl.abort();
  }, [live, tick, server?.timezone]);
  return live;
}

export interface FontOption {
  family: string;
  category: string;
  scripts: string[];
  custom: boolean;
}

/** Fonts that are really distributed (bundled + uploaded), sorted by name. */
export function useFonts(): FontOption[] {
  const [fonts, setFonts] = useState<FontOption[]>([]);
  useEffect(() => {
    let alive = true;
    void (async () => {
      const m: FontManifest | null = await loadFontManifest();
      const bundled: FontOption[] = (m?.families ?? []).map((f) => ({ family: f.family, category: f.category, scripts: f.scripts, custom: false }));
      // uploaded fonts (none when the endpoint is unavailable: only bundled fonts are offered)
      const custom: FontOption[] = (await loadCustomFonts(true)).map((family) => ({ family, category: 'custom', scripts: [], custom: true }));
      if (alive) setFonts([...bundled, ...custom].sort((a, b) => byFamily(a.family, b.family)));
    })();
    return () => {
      alive = false;
    };
  }, []);
  return fonts;
}

/** Render environment of the editor. The store clones the whole document on every change, so
 * the environment is keyed on the typography's content: it changes exactly when the
 * presentation text styles do, and the canvas rebuilds its elements then (and only then). */
export function useRenderEnv(doc: Presentation, live: LiveStore): RenderEnv {
  const typography = JSON.stringify(doc.typography);
  return useMemo(() => ({ typography: JSON.parse(typography) as Typography, assets: editorAssets, live, mode: 'edit' as const }), [typography, live]);
}
