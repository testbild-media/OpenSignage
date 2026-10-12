import { describe, expect, it } from 'vitest';
import { categoryOptions, categoryPath, categoryTree, type Category, type Product } from './types';
import { applyStatus } from './CategoryPage';
import { reorderLocal } from './CategoriesPage';
import { playlistOptions, type PlaylistSummary } from '../zones/types';

const cats: Category[] = [
  { id: 'a', name: 'Cocktails', sort: 0, productCount: 2, revision: 1 },
  { id: 'a1', parentId: 'a', name: 'Classics', sort: 0, productCount: 1, revision: 3 },
  { id: 'b', name: 'Beer', sort: 1, productCount: 0, revision: 1 },
];

describe('categories', () => {
  it('builds the two-level tree', () => {
    const tree = categoryTree(cats);
    expect(tree.map((c) => c.id)).toEqual(['a', 'b']);
    expect(tree[0]?.children.map((c) => c.id)).toEqual(['a1']);
  });
  it('labels paths and options', () => {
    expect(categoryPath(cats, 'a1')).toBe('Cocktails › Classics');
    expect(categoryPath(cats, 'x')).toBe('');
    expect(categoryOptions(cats).map((o) => o.label)).toEqual(['Cocktails', 'Cocktails › Classics', 'Beer']);
  });
  it('keeps the revision when reordering locally (the next PATCH needs it)', () => {
    const moved = reorderLocal(cats, ['b', 'a']);
    expect(moved.map((c) => c.id)).toEqual(['b', 'a', 'a1']);
    expect(moved.map((c) => c.revision)).toEqual([1, 1, 3]);
    expect(moved.find((c) => c.id === 'a1')).toEqual(cats[1]);
  });
});

describe('applyStatus', () => {
  const p: Product = {
    id: 'p',
    name: 'Mojito',
    description: '',
    categoryId: 'a',
    type: 'drink',
    price: { amount: 850, currency: 'EUR' },
    status: 'AVAILABLE',
    zoneStatus: { z1: 'SOLD_OUT' },
    showImageInsteadOfName: false,
    imageScale: 100,
    sort: 0,
    archived: false,
    revision: 1,
    createdAt: '',
    updatedAt: '',
  };
  it('sets the global status', () => {
    expect(applyStatus([p], { productId: 'p', status: 'SOLD_OUT', override: null, effective: 'SOLD_OUT', changed: true })[0]?.status).toBe('SOLD_OUT');
  });
  it('sets and clears zone overrides', () => {
    const set = applyStatus([p], { productId: 'p', zoneId: 'z2', status: 'AVAILABLE', override: 'UNAVAILABLE', effective: 'UNAVAILABLE', changed: true });
    expect(set[0]?.zoneStatus).toEqual({ z1: 'SOLD_OUT', z2: 'UNAVAILABLE' });
    const cleared = applyStatus([p], { productId: 'p', zoneId: 'z1', status: 'AVAILABLE', override: null, effective: 'AVAILABLE', changed: true });
    expect(cleared[0]?.zoneStatus).toEqual({});
    expect(p.zoneStatus).toEqual({ z1: 'SOLD_OUT' }); // input untouched
  });
});

describe('playlistOptions', () => {
  const list: PlaylistSummary[] = [
    { id: '1', name: 'Day', itemCount: 3, publishedVersion: 2, hasChanges: false, archived: false, updatedAt: '' },
    { id: '2', name: 'Draft', itemCount: 1, publishedVersion: null, hasChanges: true, archived: false, updatedAt: '' },
    { id: '3', name: 'Old', itemCount: 1, publishedVersion: 1, hasChanges: false, archived: true, updatedAt: '' },
  ];
  it('offers only published, non-archived playlists', () => {
    expect(playlistOptions(list, '', 'none', (n) => `${n}!`).map((o) => o.value)).toEqual(['', '1']);
  });
  it('keeps a current but unplayable playlist visible', () => {
    expect(playlistOptions(list, '2', 'none', (n) => `${n}!`).at(-1)).toEqual({ value: '2', label: 'Draft!' });
  });
});
