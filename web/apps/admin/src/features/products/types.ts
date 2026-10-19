// API shapes of the catalog module (internal/server/catalog) used by the admin pages.
import type { Money, ProductStatus } from '@opensignage/contracts';

export type ProductType = 'drink' | 'food' | 'other';
export const PRODUCT_TYPES: ProductType[] = ['drink', 'food', 'other'];
export const QUANTITY_UNITS = ['ml', 'cl', 'l', 'g', 'kg', 'pcs'] as const;
export const STATUSES: ProductStatus[] = ['AVAILABLE', 'SOLD_OUT', 'UNAVAILABLE'];

export interface Category {
  id: string;
  parentId?: string;
  name: string;
  sort: number;
  /** Non-archived products directly in this category. */
  productCount: number;
  /** Optimistic concurrency: every PATCH sends the revision it is based on. */
  revision: number;
}

export interface Product {
  id: string;
  name: string;
  description: string;
  categoryId: string;
  type: ProductType;
  quantity?: { value: string; unit: string };
  price: Money;
  status: ProductStatus;
  /** zoneId → override status. */
  zoneStatus: Record<string, ProductStatus>;
  imageAssetId?: string;
  showImageInsteadOfName: boolean;
  /** Logo size in percent of the automatic size (image instead of the name). */
  imageScale: number;
  /** Variants share the price and are shown like the description; each has its own status. */
  variants: Variant[];
  sort: number;
  archived: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
}

/** One variant of a product ("Orange") with its global status and zone overrides. */
export interface Variant {
  id: string;
  name: string;
  status: ProductStatus;
  /** zoneId → override status. */
  zoneStatus: Record<string, ProductStatus>;
}

/** Answer of POST /api/v1/live/products/{id}/status and /api/v1/live/variants/{id}/status. */
export interface StatusResult {
  productId: string;
  /** Set when the status of a variant changed. */
  variantId?: string;
  zoneId?: string;
  status: ProductStatus;
  override: ProductStatus | null;
  effective: ProductStatus;
  changed: boolean;
}

export interface CategoryNode extends Category {
  children: Category[];
}

/** Builds the two-level tree from the server order (parents followed by their children). */
export function categoryTree(cats: Category[]): CategoryNode[] {
  const tops: CategoryNode[] = cats.filter((c) => !c.parentId).map((c) => ({ ...c, children: [] }));
  const byId = new Map(tops.map((c) => [c.id, c]));
  for (const c of cats) {
    if (c.parentId) byId.get(c.parentId)?.children.push(c);
  }
  return tops;
}

/** "Parent › Child" label of a category. */
export function categoryPath(cats: Category[], id: string): string {
  const c = cats.find((x) => x.id === id);
  if (!c) return '';
  const parent = c.parentId ? cats.find((x) => x.id === c.parentId) : undefined;
  return parent ? `${parent.name} › ${c.name}` : c.name;
}

/** Options for a category select: top-level categories followed by their children. */
export function categoryOptions(cats: Category[]): { value: string; label: string }[] {
  return categoryTree(cats).flatMap((top) => [{ value: top.id, label: top.name }, ...top.children.map((ch) => ({ value: ch.id, label: `${top.name} › ${ch.name}` }))]);
}
