// Per-element typography of product lists and single products: partial marks on top of the
// presentation typography (ProductOverrides). The renderer merges them with mergeStyle.
import type { Marks, ProductOverrides, TextStyle, Typography } from '@opensignage/contracts';
import { mergeStyle } from '@opensignage/render';

export type OverrideRole = keyof ProductOverrides;
export type OverrideKey = 'font' | 'size' | 'color' | 'bold' | 'italic';

/** Text roles a product list draws (headings only with category headings). */
export const LIST_ROLES: OverrideRole[] = ['heading', 'subheading', 'item', 'description', 'price'];
/** A single product has no headings. */
export const PRODUCT_ROLES: OverrideRole[] = ['item', 'description', 'price'];

/**
 * Sets one property of one role (or clears it with undefined). Empty roles and an empty
 * override object are removed, so "reset" leaves the element exactly as without overrides.
 */
export function setOverride<K extends OverrideKey>(o: ProductOverrides | undefined, role: OverrideRole, key: K, value: Marks[K] | undefined): ProductOverrides | undefined {
  const next: ProductOverrides = { ...o };
  const m: Marks = { ...next[role] };
  if (value === undefined) delete m[key];
  else m[key] = value;
  if (Object.keys(m).length > 0) next[role] = m;
  else delete next[role];
  return Object.keys(next).length > 0 ? next : undefined;
}

/** Removes all overrides of one role (back to the presentation typography). */
export function clearRole(o: ProductOverrides | undefined, role: OverrideRole): ProductOverrides | undefined {
  if (!o) return undefined;
  const next: ProductOverrides = { ...o };
  delete next[role];
  return Object.keys(next).length > 0 ? next : undefined;
}

export function isOverridden(o: ProductOverrides | undefined, role: OverrideRole, key?: OverrideKey): boolean {
  const m = o?.[role];
  if (!m) return false;
  return key ? m[key] !== undefined : Object.keys(m).length > 0;
}

/** The style the renderer uses for a role (before the element scale). */
export function effectiveStyle(typo: Typography, o: ProductOverrides | undefined, role: OverrideRole): TextStyle {
  return mergeStyle(typo[role], o?.[role]);
}
