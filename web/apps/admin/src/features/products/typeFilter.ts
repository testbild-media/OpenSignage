// Product type filter (type drink / food / other). At a bar the bar
// staff look for drinks and the kitchen for food, so the admin product list and the
// operator UI filter by type, combinable with the category filter.
import { PRODUCT_TYPES, type ProductType } from './types';

export type TypeFilter = 'all' | ProductType;

/** Chip labels (plural, "Drinks"); the singular type names live in admin.products.type.*. */
export const TYPE_FILTER_LABELS: Record<TypeFilter, string> = {
  all: 'admin.products.filter.all',
  drink: 'admin.products.filter.drink',
  food: 'admin.products.filter.food',
  other: 'admin.products.filter.other',
};

export function isTypeFilter(v: unknown): v is TypeFilter {
  return v === 'all' || PRODUCT_TYPES.some((x) => x === v);
}

interface Typed {
  type?: string;
}

/** Products without a known type count as "other" (the server never stores another value). */
export function productType(p: Typed): ProductType {
  return PRODUCT_TYPES.find((x) => x === p.type) ?? 'other';
}

export function matchesType(p: Typed, filter: TypeFilter): boolean {
  return filter === 'all' || productType(p) === filter;
}

export function filterByType<T extends Typed>(list: T[], filter: TypeFilter): T[] {
  return filter === 'all' ? list : list.filter((p) => matchesType(p, filter));
}

/** Number of products per type, in the fixed type order. */
export function typeCounts(list: Typed[]): Record<ProductType, number> {
  const out: Record<ProductType, number> = { drink: 0, food: 0, other: 0 };
  for (const p of list) out[productType(p)]++;
  return out;
}

/**
 * The chips to offer: "all" plus every type that occurs. No chips when the list has a
 * single type (a filter would change nothing) – unless a filter is active, which must stay
 * visible so it can be switched off again.
 */
export function typeChips(list: Typed[], current: TypeFilter): { value: TypeFilter; count: number }[] {
  const counts = typeCounts(list);
  const present = PRODUCT_TYPES.filter((x) => counts[x] > 0 || x === current);
  if (present.length < 2 && current === 'all') return [];
  return [{ value: 'all', count: list.length }, ...present.map((x) => ({ value: x, count: counts[x] }))];
}

/**
 * Sorting while a filter hides products: the visible products take the new order among the
 * places they had; hidden products keep their places. Returns the full new order (the server
 * moves unlisted products to the end, so it always gets the complete list).
 */
export function mergeFilteredOrder(all: string[], visibleNewOrder: string[]): string[] {
  const visible = new Set(visibleNewOrder);
  let next = 0;
  return all.map((id) => (visible.has(id) ? visibleNewOrder[next++] ?? id : id));
}
