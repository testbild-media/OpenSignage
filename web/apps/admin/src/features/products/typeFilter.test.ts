import { describe, expect, it } from 'vitest';
import { filterByType, isTypeFilter, matchesType, mergeFilteredOrder, productType, typeChips, typeCounts } from './typeFilter';

const list = [
  { id: 'mojito', type: 'drink' },
  { id: 'burger', type: 'food' },
  { id: 'beer', type: 'drink' },
  { id: 'voucher', type: 'other' },
];

describe('product type filter', () => {
  it('filters by type, "all" keeps everything', () => {
    expect(filterByType(list, 'drink').map((p) => p.id)).toEqual(['mojito', 'beer']);
    expect(filterByType(list, 'food').map((p) => p.id)).toEqual(['burger']);
    expect(filterByType(list, 'all')).toBe(list);
  });

  it('treats an unknown type as "other"', () => {
    expect(productType({})).toBe('other');
    expect(productType({ type: 'cocktail' })).toBe('other');
    expect(matchesType({ type: undefined }, 'other')).toBe(true);
    expect(matchesType({ type: 'drink' }, 'all')).toBe(true);
  });

  it('accepts only known filters from storage', () => {
    expect(['all', 'drink', 'food', 'other'].every(isTypeFilter)).toBe(true);
    expect(isTypeFilter('cocktail')).toBe(false);
    expect(isTypeFilter(null)).toBe(false);
  });

  it('counts per type', () => {
    expect(typeCounts(list)).toEqual({ drink: 2, food: 1, other: 1 });
  });

  it('offers chips only for the types that occur, with counts', () => {
    expect(typeChips(list, 'all')).toEqual([
      { value: 'all', count: 4 },
      { value: 'drink', count: 2 },
      { value: 'food', count: 1 },
      { value: 'other', count: 1 },
    ]);
    const drinksAndFood = list.filter((p) => p.type !== 'other');
    expect(typeChips(drinksAndFood, 'all').map((c) => c.value)).toEqual(['all', 'drink', 'food']);
  });

  it('hides the chips when a single type occurs, unless a filter is active', () => {
    const drinks = filterByType(list, 'drink');
    expect(typeChips(drinks, 'all')).toEqual([]);
    expect(typeChips([], 'all')).toEqual([]);
    // An active filter stays switchable even where nothing of that type exists.
    expect(typeChips(drinks, 'food')).toEqual([
      { value: 'all', count: 2 },
      { value: 'drink', count: 2 },
      { value: 'food', count: 0 },
    ]);
  });

  it('sorting a filtered list moves only the visible products among their places', () => {
    // a(drink) b(food) c(drink) d(food) e(drink); drinks shown and reordered to e, a, c
    expect(mergeFilteredOrder(['a', 'b', 'c', 'd', 'e'], ['e', 'a', 'c'])).toEqual(['e', 'b', 'a', 'd', 'c']);
    // without a filter the new order is the order
    expect(mergeFilteredOrder(['a', 'b', 'c'], ['c', 'a', 'b'])).toEqual(['c', 'a', 'b']);
  });
});
