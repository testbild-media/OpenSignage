// Price rows of a time schedule rule: single products or whole categories (with their
// subcategories). The server turns category prices into product prices for the screens.
import type { PriceOverride } from '@opensignage/contracts';
import { formatMoney } from '@opensignage/i18n';
import { categoryTree, type Category, type Product } from '../products/types';

/** Value of a price row in the selection: "p:<product id>" or "c:<category id>". */
export const targetOf = (p: PriceOverride): string => (p.categoryId ? `c:${p.categoryId}` : `p:${p.productId ?? ''}`);

/** Categories in the order of the catalog: each top level category followed by its subcategories. */
export function catalogOrder(categories: Category[]): Category[] {
  return categoryTree(categories).flatMap((c) => [c, ...c.children]);
}

/** Products of the category and of its subcategories. */
export function productsIn(categoryId: string, products: Product[], categories: Category[]): Product[] {
  const ids = new Set([categoryId, ...categories.filter((c) => c.parentId === categoryId).map((c) => c.id)]);
  return products.filter((p) => ids.has(p.categoryId));
}

/** Whole categories first (in the order of the catalog), then the products as stored. */
export function sortPrices(prices: PriceOverride[], categories: Category[]): PriceOverride[] {
  const order = new Map(catalogOrder(categories).map((c, i) => [c.id, i]));
  const rank = (p: PriceOverride): number => (p.categoryId ? (order.get(p.categoryId) ?? -1) : Number.MAX_SAFE_INTEGER);
  return [...prices].sort((a, b) => rank(a) - rank(b));
}

/** "7,50 € – 8,50 €" of the base prices (one price when all are equal, "" for none). */
export function moneyRange(products: Product[], lang: string): string {
  const first = products[0];
  if (!first) return '';
  const amounts = products.map((p) => p.price.amount);
  const lo = Math.min(...amounts);
  const hi = Math.max(...amounts);
  const currency = first.price.currency;
  return lo === hi ? formatMoney(lo, currency, lang) : `${formatMoney(lo, currency, lang)} – ${formatMoney(hi, currency, lang)}`;
}
