// Category overview with product counts, category/subcategory CRUD and drag & drop sorting.
import { type ReactNode, useCallback, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { Archive, FolderPlus, GripVertical, Pencil, Plus, Trash2 } from 'lucide-react';
import { del, errorText, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Button, DataState, ErrorText, IconButton, PageHeader } from '../../components/ui';
import { useRights } from '../../lib/rights';
import { useSortable } from '../../components/sortable';
import { CategoryDialog } from './CategoryDialog';
import { ProductDialog } from './ProductDialog';
import { categoryTree, type Category, type CategoryNode } from './types';

type Editing = { category: Category | null; parentId?: string } | null;

/** Applies a new sibling order locally so the list does not jump back while saving. */
export function reorderLocal(cats: Category[], ids: string[]): Category[] {
  const pos = new Map(ids.map((id, i) => [id, i]));
  const sorted = cats.map((c) => (pos.has(c.id) ? { ...c, sort: pos.get(c.id)! } : c));
  const tree = categoryTree(sorted);
  tree.sort((a, b) => a.sort - b.sort);
  return tree.flatMap((top) => [top as Category, ...[...top.children].sort((a, b) => a.sort - b.sort)]).map(stripChildren);
}

function stripChildren(c: Category | CategoryNode): Category {
  const { id, parentId, name, sort, productCount, revision } = c;
  return parentId ? { id, parentId, name, sort, productCount, revision } : { id, name, sort, productCount, revision };
}

export function useCategoryActions(reload: () => void) {
  const t = useT();
  const confirm = useConfirm();
  const toast = useToast();
  return useAction(async (c: Category, cats: Category[]) => {
    const subs = cats.filter((x) => x.parentId === c.id).length;
    if (c.productCount > 0 || subs > 0) {
      toast(t('admin.products.category.notEmpty', { name: c.name, products: c.productCount, subcategories: subs }), 'error');
      return;
    }
    const ok = await confirm({
      title: t('admin.products.category.delete'),
      message: t('admin.products.category.deleteConfirm', { name: c.name }),
      confirmLabel: t('common.action.delete'),
      danger: true,
    });
    if (!ok) return;
    await del(`/api/v1/categories/${c.id}`);
    toast(t('admin.products.category.deleted'));
    reload();
  });
}

function SortableGroup({
  items,
  render,
  onReorder,
  label,
}: {
  items: Category[];
  render: (c: Category, handle: ReturnType<ReturnType<typeof useSortable>['handleProps']>) => ReactNode;
  onReorder: (ids: string[]) => void;
  label: string;
}) {
  const ids = items.map((c) => c.id);
  const sort = useSortable(ids, onReorder);
  return (
    <>
      {items.map((c) => (
        <div key={c.id} className="cat-item" {...sort.itemProps(c.id)}>
          {render(c, sort.handleProps(c.id, label))}
        </div>
      ))}
    </>
  );
}

export function CategoriesPage() {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const q = useQuery<{ categories: Category[] }>('/api/v1/categories');
  useEvents(['products'], () => q.reload());
  const edit = useRights().canEditProducts;
  const [editing, setEditing] = useState<Editing>(null);
  const [newProduct, setNewProduct] = useState(false);
  const cats = q.data?.categories ?? [];
  const tree = categoryTree(cats);
  const remove = useCategoryActions(q.reload);

  const reorder = useAction(async (ids: string[]) => {
    const before = q.data;
    if (before) q.setData({ categories: reorderLocal(before.categories, ids) });
    try {
      await post('/api/v1/categories/reorder', { ids });
    } catch (e) {
      if (before) q.setData(before);
      toast(errorText(e), 'error');
    } finally {
      q.reload();
    }
  });

  const closeEditor = useCallback(() => setEditing(null), []);
  const closeProduct = useCallback(() => setNewProduct(false), []);
  const total = (c: CategoryNode): number => c.productCount + c.children.reduce((n, ch) => n + ch.productCount, 0);
  const moveLabel = t('admin.products.dragToSort');

  return (
    <div className="page">
      <PageHeader
        title={t('admin.products.title')}
        subtitle={t('admin.products.subtitle')}
        actions={
          <>
            <Link to="/products/archive" className="btn btn-secondary">
              <Archive size={16} />
              <span>{t('admin.products.archive.title')}</span>
            </Link>
            {edit && (
              <>
                <Button icon={<FolderPlus size={16} />} onClick={() => setEditing({ category: null })}>
                  {t('admin.products.category.add')}
                </Button>
                <Button variant="primary" icon={<Plus size={16} />} disabled={cats.length === 0} onClick={() => setNewProduct(true)}>
                  {t('admin.products.add')}
                </Button>
              </>
            )}
          </>
        }
      />
      <ErrorText error={remove.error} />
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={tree.length === 0} emptyText={t('admin.products.category.empty')}>
        <div className="cat-list" aria-busy={reorder.busy || undefined}>
          <SortableGroup
            items={tree}
            label={moveLabel}
            onReorder={(ids) => void reorder.run(ids)}
            render={(top, handle) => {
              const node = tree.find((x) => x.id === top.id)!;
              return (
                <div className="cat-card">
                  <div className="cat-row cat-row-top">
                    {edit && (
                      <button type="button" {...handle}>
                        <GripVertical size={16} />
                      </button>
                    )}
                    <Link to={`/products/category/${node.id}`} className="cat-name">
                      {node.name}
                    </Link>
                    <span className="cat-count">
                      {t('admin.products.count', { count: total(node) })}
                      {node.children.length > 0 && <span className="faint"> · {t('admin.products.subCount', { count: node.children.length })}</span>}
                    </span>
                    {edit && (
                      <span className="row-actions">
                        <IconButton label={t('admin.products.category.addSub')} onClick={() => setEditing({ category: null, parentId: node.id })}>
                          <FolderPlus size={16} />
                        </IconButton>
                        <IconButton label={t('common.action.edit')} onClick={() => setEditing({ category: node })}>
                          <Pencil size={16} />
                        </IconButton>
                        <IconButton label={t('common.action.delete')} disabled={remove.busy} onClick={() => void remove.run(node, cats)}>
                          <Trash2 size={16} />
                        </IconButton>
                      </span>
                    )}
                  </div>
                  {node.children.length > 0 && (
                    <div className="cat-children">
                      <SortableGroup
                        items={node.children}
                        label={moveLabel}
                        onReorder={(ids) => void reorder.run(ids)}
                        render={(ch, h) => (
                          <div className="cat-row">
                            {edit && (
                              <button type="button" {...h}>
                                <GripVertical size={14} />
                              </button>
                            )}
                            <Link to={`/products/category/${ch.id}`} className="cat-name">
                              {ch.name}
                            </Link>
                            <span className="cat-count">{t('admin.products.count', { count: ch.productCount })}</span>
                            {edit && (
                              <span className="row-actions">
                                <IconButton label={t('common.action.edit')} onClick={() => setEditing({ category: ch })}>
                                  <Pencil size={16} />
                                </IconButton>
                                <IconButton label={t('common.action.delete')} disabled={remove.busy} onClick={() => void remove.run(ch, cats)}>
                                  <Trash2 size={16} />
                                </IconButton>
                              </span>
                            )}
                          </div>
                        )}
                      />
                    </div>
                  )}
                </div>
              );
            }}
          />
        </div>
        <p className="faint cat-hint">{t('admin.products.category.sortHint')}</p>
      </DataState>
      {editing && (
        <CategoryDialog
          category={editing.category}
          parentId={editing.parentId}
          categories={cats}
          onClose={closeEditor}
          onSaved={() => {
            setEditing(null);
            q.reload();
          }}
        />
      )}
      {newProduct && (
        <ProductDialog
          product={null}
          categories={cats}
          onClose={closeProduct}
          onSaved={(p) => {
            setNewProduct(false);
            navigate(`/products/category/${p.categoryId}`);
          }}
        />
      )}
    </div>
  );
}
