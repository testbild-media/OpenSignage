import { describe, expect, it } from 'vitest';
import type { Category, Product } from '../products/types';
import { catalogOrder, moneyRange, productsIn, sortPrices, targetOf } from './prices';

const cat = (id: string, sort: number, parentId?: string): Category => ({ id, name: id, sort, parentId, productCount: 0, revision: 1 });
const prod = (id: string, categoryId: string, amount: number): Product => ({ id, categoryId, price: { amount, currency: 'EUR' } }) as Product;

// In the order of the server; subcategories follow their parent in the catalog order.
const categories = [cat('longdrinks', 1), cat('beer', 2), cat('vodka', 1, 'longdrinks'), cat('gin', 2, 'longdrinks')];
const products = [prod('gt', 'gin', 800), prod('cuba', 'longdrinks', 850), prod('vo', 'vodka', 750), prod('pils', 'beer', 400)];

describe('category prices', () => {
  it('encodes the row target', () => {
    expect(targetOf({ categoryId: 'x', price: { amount: 1, currency: 'EUR' } })).toBe('c:x');
    expect(targetOf({ productId: 'y', price: { amount: 1, currency: 'EUR' } })).toBe('p:y');
  });

  it('includes the products of subcategories', () => {
    expect(productsIn('longdrinks', products, categories).map((p) => p.id)).toEqual(['gt', 'cuba', 'vo']);
    expect(productsIn('vodka', products, categories).map((p) => p.id)).toEqual(['vo']);
  });

  it('lists categories in the order of the catalog', () => {
    expect(catalogOrder(categories).map((c) => c.id)).toEqual(['longdrinks', 'vodka', 'gin', 'beer']);
  });

  it('puts whole categories first', () => {
    const m = { amount: 500, currency: 'EUR' };
    const sorted = sortPrices([{ productId: 'pils', price: m }, { categoryId: 'beer', price: m }, { productId: 'cuba', price: m }, { categoryId: 'vodka', price: m }], categories);
    expect(sorted.map(targetOf)).toEqual(['c:vodka', 'c:beer', 'p:pils', 'p:cuba']);
  });

  it('shows the range of the base prices', () => {
    const range = moneyRange(productsIn('longdrinks', products, categories), 'en');
    expect(range).toContain('7.50');
    expect(range).toContain('8.50');
    expect(moneyRange([prod('a', 'x', 500), prod('b', 'x', 500)], 'en')).not.toContain('–');
    expect(moneyRange([], 'en')).toBe('');
  });
});
