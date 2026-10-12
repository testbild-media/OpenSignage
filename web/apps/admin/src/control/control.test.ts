import { describe, expect, it } from 'vitest';
import { applyZoneStatus, carriedProducts, categoryChoices, filterSections, productSections, type LiveZones } from './Control';

const data: LiveZones = {
  serverTime: '',
  categories: [
    { id: 'a', name: 'Cocktails', sort: 0 },
    { id: 'a1', parentId: 'a', name: 'Classics', sort: 0 },
    { id: 'b', name: 'Empty', sort: 1 },
  ],
  products: [
    { id: 'p1', name: 'Mojito', description: '', categoryId: 'a1', price: { amount: 850, currency: 'EUR' }, status: 'AVAILABLE', sort: 0 },
    { id: 'p2', name: 'Spritz', description: '', categoryId: 'a', price: { amount: 750, currency: 'EUR' }, status: 'AVAILABLE', sort: 0 },
  ],
  zones: [
    {
      id: 'z',
      name: 'Bar',
      state: 'OPEN',
      hasClosedPlaylist: false,
      products: [
        { id: 'p1', status: 'AVAILABLE', override: null },
        { id: 'p2', status: 'AVAILABLE', override: null },
      ],
    },
  ],
};

describe('productSections', () => {
  it('orders top-level before subcategories and skips empty ones', () => {
    const s = productSections(data);
    expect(s.map((x) => x.category.id)).toEqual(['a', 'a1']);
    expect(s[1]?.parent?.id).toBe('a');
  });
});

describe('applyZoneStatus', () => {
  it('applies only the confirmed answer to the zone', () => {
    const next = applyZoneStatus(data, 'z', { productId: 'p1', zoneId: 'z', status: 'AVAILABLE', override: 'SOLD_OUT', effective: 'SOLD_OUT' });
    expect(next.zones[0]?.products[0]).toEqual({ id: 'p1', status: 'SOLD_OUT', override: 'SOLD_OUT' });
    expect(next.zones[0]?.products[1]?.override).toBeNull();
    expect(data.zones[0]?.products[0]?.override).toBeNull();
  });
});

describe('product filters in the operator UI', () => {
  const bar: LiveZones = {
    ...data,
    categories: [
      { id: 'a', name: 'Cocktails', sort: 0 },
      { id: 'a1', parentId: 'a', name: 'Classics', sort: 0 },
      { id: 'k', name: 'Kitchen', sort: 1 },
    ],
    products: [
      { id: 'p1', name: 'Mojito', description: '', categoryId: 'a1', type: 'drink', price: { amount: 850, currency: 'EUR' }, status: 'AVAILABLE', sort: 0 },
      { id: 'p2', name: 'Spritz', description: '', categoryId: 'a', type: 'drink', price: { amount: 750, currency: 'EUR' }, status: 'AVAILABLE', sort: 0 },
      { id: 'p3', name: 'Burger', description: '', categoryId: 'k', type: 'food', price: { amount: 1250, currency: 'EUR' }, status: 'AVAILABLE', sort: 0 },
      { id: 'p4', name: 'Nachos', description: '', categoryId: 'a', type: 'food', price: { amount: 650, currency: 'EUR' }, status: 'AVAILABLE', sort: 1 },
    ],
  };
  const sections = productSections(bar);
  const ids = (f: Parameters<typeof filterSections>[1]): string[] => filterSections(sections, f).flatMap((s) => s.products.map((p) => p.id));

  it('filters by type', () => {
    expect(ids({ type: 'all', category: 'all', search: '' })).toEqual(['p2', 'p4', 'p1', 'p3']);
    expect(ids({ type: 'drink', category: 'all', search: '' })).toEqual(['p2', 'p1']);
    expect(ids({ type: 'food', category: 'all', search: '' })).toEqual(['p4', 'p3']);
  });

  it('combines type, category (with subcategories) and search', () => {
    expect(ids({ type: 'drink', category: 'a', search: '' })).toEqual(['p2', 'p1']);
    expect(ids({ type: 'food', category: 'a', search: '' })).toEqual(['p4']);
    expect(ids({ type: 'food', category: 'k', search: 'bur' })).toEqual(['p3']);
    expect(ids({ type: 'drink', category: 'k', search: '' })).toEqual([]);
    expect(filterSections(sections, { type: 'drink', category: 'a', search: '' }).map((s) => s.category.id)).toEqual(['a', 'a1']);
  });

  it('offers only the categories that have products of the chosen type', () => {
    expect(categoryChoices(bar.categories, sections, 'all').map((c) => c.id)).toEqual(['a', 'k']);
    expect(categoryChoices(bar.categories, sections, 'drink').map((c) => c.id)).toEqual(['a']);
    expect(categoryChoices(bar.categories, sections, 'food').map((c) => c.id)).toEqual(['a', 'k']);
    expect(categoryChoices(bar.categories, sections, 'other')).toEqual([]);
  });
});

describe('carriedProducts', () => {
  it('leaves out products that are not available in the zone (not carried), using the zone override', () => {
    const products = [
      { id: 'a', status: 'AVAILABLE' as const },
      { id: 'b', status: 'UNAVAILABLE' as const },
      { id: 'c', status: 'SOLD_OUT' as const },
      { id: 'd', status: 'UNAVAILABLE' as const },
    ];
    const zone = [
      { id: 'c', status: 'UNAVAILABLE' as const }, // not carried in this zone
      { id: 'd', status: 'AVAILABLE' as const }, // carried here only
    ];
    expect(carriedProducts(products, zone).map((p) => p.id)).toEqual(['a', 'd']);
  });
});
