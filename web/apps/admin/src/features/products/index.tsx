import { useMemo } from 'react';
import { Package } from 'lucide-react';
import type { Feature, NavChild } from '../types';
import { useQuery } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { CategoriesPage } from './CategoriesPage';
import { CategoryPage } from './CategoryPage';
import { ArchivePage } from './ArchivePage';
import type { Category } from './types';
import '../../components/controls.css';
import './products.css';

/** Sidebar children: the real top-level categories. */
function useCategoryChildren(): NavChild[] {
  const q = useQuery<{ categories: Category[] }>('/api/v1/categories');
  useEvents(['products'], () => q.reload());
  return useMemo(
    () => (q.data?.categories ?? []).filter((c) => !c.parentId).map((c) => ({ key: c.id, label: c.name, path: `/products/category/${c.id}` })),
    [q.data],
  );
}

export const productsFeature: Feature = {
  routes: [
    { path: '/products', element: <CategoriesPage />, minRole: 'operator' },
    { path: '/products/category/:id', element: <CategoryPage />, minRole: 'operator' },
    { path: '/products/archive', element: <ArchivePage />, minRole: 'operator' },
  ],
  nav: [
    {
      key: 'products',
      label: 'admin.nav.products',
      icon: Package,
      path: '/products',
      minRole: 'operator',
      order: 20,
      sub: [{ key: 'archive', label: 'admin.products.archive.title', path: '/products/archive', minRole: 'operator' }],
      useChildren: useCategoryChildren,
    },
  ],
};
