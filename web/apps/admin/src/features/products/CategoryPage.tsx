// Products of one category (and its subcategories): fast three-state status toggle, zone
// overrides, add/edit in a dialog, archive, drag & drop sorting inside a category.
import { useCallback, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router';
import { Archive, ChevronRight, CopyPlus, FolderPlus, GripVertical, Pencil, Plus, X } from 'lucide-react';
import type { ProductStatus } from '@opensignage/contracts';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { errorText, post } from '../../lib/api';
import { useAction, useQuery, useStoredState, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useSession } from '../../lib/session';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, ErrorText, IconButton, PageHeader } from '../../components/ui';
import { MediaThumb } from '../../components/MediaPicker';
import { useSortable } from '../../components/sortable';
import type { Zone } from '../zones/types';
import { CategoryDialog } from './CategoryDialog';
import { ProductDialog } from './ProductDialog';
import { StatusToggle, useStatusChange } from './StatusToggle';
import { useRights } from '../../lib/rights';
import { decimalSeparator, formatQuantityValue } from './money';
import type { Category, Product, StatusResult } from './types';
import { filterByType, isTypeFilter, mergeFilteredOrder, TYPE_FILTER_LABELS, type TypeFilter, typeChips } from './typeFilter';

export function quantityText(p: Pick<Product, 'quantity'>, t: (k: string) => string, lang: string): string {
  if (!p.quantity) return '';
  return `${formatQuantityValue(p.quantity.value, decimalSeparator(lang))} ${t(`admin.products.unit.${p.quantity.unit}`)}`;
}

/** Applies a status answer to the product list (only after the server confirmed it). */
export function applyStatus(list: Product[], r: StatusResult): Product[] {
  return list.map((p) => {
    if (p.id !== r.productId) return p;
    if (!r.zoneId) return { ...p, status: r.status };
    const zoneStatus = { ...p.zoneStatus };
    if (r.override) zoneStatus[r.zoneId] = r.override;
    else delete zoneStatus[r.zoneId];
    return { ...p, status: r.status, zoneStatus };
  });
}

interface RowHandlers {
  zones: Zone[];
  busy: ReadonlySet<string>;
  setStatus: (id: string, s: ProductStatus | null, zoneId?: string) => void;
  onEdit: (p: Product) => void;
  onDuplicate: (p: Product) => void;
  onArchive: (p: Product) => void;
  archiving: boolean;
}

function ProductTable({ products, onReorder, h }: { products: Product[]; onReorder: (ids: string[]) => void; h: RowHandlers }) {
  const t = useT();
  // "Not available" (= not carried) is the administrators' decision.
  const isAdmin = useSession().user?.role === 'admin';
  const rights = useRights();
  const edit = rights.canEditProducts;
  const lang = getLanguage();
  const ids = products.map((p) => p.id);
  const sort = useSortable(ids, onReorder);
  const zoneName = (id: string): string => h.zones.find((z) => z.id === id)?.name ?? '?';
  return (
    <table className="table product-table product-table-fixed">
      <thead>
        <tr>
          <th className="col-handle" />
          <th>{t('admin.products.field.name')}</th>
          <th className="col-qty">{t('admin.products.field.quantity')}</th>
          <th className="num col-price">{t('admin.products.field.price')}</th>
          <th className="col-status">{t('admin.products.field.status')}</th>
          <th className="col-actions" />
        </tr>
      </thead>
      <tbody>
        {products.map((p) => {
          const busy = h.busy.has(p.id);
          const overrides = Object.entries(p.zoneStatus);
          return (
            <tr key={p.id} {...sort.itemProps(p.id)} className={`status-row-${p.status.toLowerCase()}`}>
              <td className="col-handle">
                {edit && (
                  <button type="button" {...sort.handleProps(p.id, t('admin.products.dragToSort'))}>
                    <GripVertical size={16} />
                  </button>
                )}
              </td>
              <td>
                <div className="product-cell">
                  {p.imageAssetId ? <MediaThumb assetId={p.imageAssetId} className="product-thumb" /> : <span className="product-thumb product-thumb-empty" />}
                  <div className="product-text">
                    <div className="product-name">
                      <span className="ellipsis">{p.name}</span>
                      {p.showImageInsteadOfName && <Badge>{t('admin.products.imageAsName')}</Badge>}
                    </div>
                    {p.description && <div className="product-desc">{p.description}</div>}
                  </div>
                </div>
              </td>
              <td className="nowrap muted">{quantityText(p, t, lang)}</td>
              <td className="nowrap num">{formatMoney(p.price.amount, p.price.currency, lang)}</td>
              <td>
                <StatusToggle value={p.status} busy={busy} canHide={isAdmin} readOnly={!edit} label={t('admin.products.statusOf', { name: p.name })} onChange={(s) => h.setStatus(p.id, s)} />
                {overrides.length > 0 && (
                  <div className="override-list" aria-label={t('admin.products.overrides')}>
                    {overrides.map(([zoneId, st]) => (
                      <span key={zoneId} className={`override-chip st-${st.toLowerCase()}`} title={t('admin.products.overrideTitle')}>
                        <span className="ellipsis">
                          {zoneName(zoneId)}: {t(`admin.products.status.${st}`)}
                        </span>
                        <button type="button" disabled={busy || !rights.canEditZone(zoneId)} aria-label={t('admin.products.clearOverride', { zone: zoneName(zoneId) })} title={t('admin.products.clearOverride', { zone: zoneName(zoneId) })} onClick={() => h.setStatus(p.id, null, zoneId)}>
                          <X size={12} />
                        </button>
                      </span>
                    ))}
                  </div>
                )}
              </td>
              <td className="row-actions">
                {edit && (
                  <>
                    <IconButton label={t('common.action.edit')} onClick={() => h.onEdit(p)}>
                      <Pencil size={16} />
                    </IconButton>
                    <IconButton label={t('admin.products.duplicate')} onClick={() => h.onDuplicate(p)}>
                      <CopyPlus size={16} />
                    </IconButton>
                    <IconButton label={t('admin.products.archive.action')} disabled={h.archiving} onClick={() => h.onArchive(p)}>
                      <Archive size={16} />
                    </IconButton>
                  </>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

/** Type filter chips ("All types / Drinks / Food / Other") with the number of products. */
function TypeFilterBar({ products, value, onChange }: { products: Product[]; value: TypeFilter; onChange: (v: TypeFilter) => void }) {
  const t = useT();
  const chips = typeChips(products, value);
  if (chips.length === 0) return null;
  return (
    <div className="product-filter">
      <div className="segmented" role="group" aria-label={t('admin.products.field.type')}>
        {chips.map((c) => (
          <button key={c.value} type="button" className={value === c.value ? 'active' : ''} aria-pressed={value === c.value} onClick={() => onChange(c.value)}>
            {t(TYPE_FILTER_LABELS[c.value])}
            <span className="product-filter-count">{c.count}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

export function CategoryPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const { id = '' } = useParams();
  const cats = useQuery<{ categories: Category[] }>('/api/v1/categories');
  const prods = useQuery<{ products: Product[] }>(`/api/v1/products?category=${encodeURIComponent(id)}`);
  const zones = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  useEvents(['products'], () => {
    cats.reload();
    prods.reload();
  });
  useEvents(['live', 'zones'], (e) => {
    if (e.topic === '*' || e.topic === 'zones' || e.kind?.startsWith('zone.')) zones.reload();
    if (e.topic === '*' || e.kind === 'product.status') prods.reload();
  });
  const canEditProducts = useRights().canEditProducts;
  const [editing, setEditing] = useState<Product | 'new' | { copyOf: Product } | null>(null);
  const [catEdit, setCatEdit] = useState<{ category: Category | null; parentId?: string } | null>(null);
  // Remembered per browser and kept across categories: whoever looks for drinks keeps
  // looking for drinks. The chips stay visible while a filter is active.
  const [storedType, setTypeFilter] = useStoredState<string>('os.products.typeFilter', 'all');
  const typeFilter: TypeFilter = isTypeFilter(storedType) ? storedType : 'all';

  const all = useMemo(() => cats.data?.categories ?? [], [cats.data]);
  const category = all.find((c) => c.id === id);
  const parent = category?.parentId ? all.find((c) => c.id === category.parentId) : undefined;
  const children = category && !category.parentId ? all.filter((c) => c.parentId === category.id) : [];
  const list = prods.data?.products ?? [];

  const status = useStatusChange((r) => {
    if (prods.data) prods.setData({ products: applyStatus(prods.data.products, r) });
  });
  const archive = useAction(async (p: Product) => {
    const ok = await confirm({ title: t('admin.products.archive.action'), message: t('admin.products.archive.confirm', { name: p.name }), confirmLabel: t('admin.products.archive.action') });
    if (!ok) return;
    await post(`/api/v1/products/${p.id}/archive`);
    toast(t('admin.products.archive.done', { name: p.name }));
    prods.reload();
    cats.reload();
  });
  const reorder = useAction(async (ids: string[]) => {
    const before = prods.data;
    if (before) {
      const pos = new Map(ids.map((x, i) => [x, i]));
      prods.setData({ products: before.products.map((p) => (pos.has(p.id) ? { ...p, sort: pos.get(p.id)! } : p)) });
    }
    try {
      await post('/api/v1/products/reorder', { ids });
    } catch (e) {
      if (before) prods.setData(before);
      toast(errorText(e), 'error');
    } finally {
      prods.reload();
    }
  });

  const closeEditor = useCallback(() => setEditing(null), []);
  const closeCat = useCallback(() => setCatEdit(null), []);

  const handlers: RowHandlers = {
    zones: zones.data?.zones ?? [],
    busy: status.busy,
    setStatus: (pid, s, zoneId) => void status.run(pid, s, zoneId),
    onEdit: setEditing,
    onDuplicate: (p) => setEditing({ copyOf: p }),
    onArchive: (p) => void archive.run(p),
    archiving: archive.busy,
  };
  const inCat = (cid: string): Product[] =>
    list
      .map((p, i) => ({ p, i }))
      .filter((x) => x.p.categoryId === cid)
      .sort((a, b) => a.p.sort - b.p.sort || a.i - b.i)
      .map((x) => x.p);
  const own = category ? inCat(category.id) : [];
  const notFound = !!cats.data && !category;
  const filtering = typeFilter !== 'all';
  const ownShown = filterByType(own, typeFilter);
  const childSections = children.map((ch) => ({ ch, items: filterByType(inCat(ch.id), typeFilter) }));
  const nothingOfType = filtering && ownShown.length === 0 && childSections.every((s) => s.items.length === 0);
  // Sorting a filtered list moves the visible products among their places only.
  const sortIn = (cid: string) => (ids: string[]) => void reorder.run(mergeFilteredOrder(inCat(cid).map((p) => p.id), ids));

  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/products">{t('admin.products.title')}</Link>
        {parent && (
          <>
            <ChevronRight size={14} />
            <Link to={`/products/category/${parent.id}`}>{parent.name}</Link>
          </>
        )}
      </div>
      <PageHeader
        title={category?.name ?? (notFound ? t('admin.products.category.notFound') : '')}
        subtitle={category ? t('admin.products.count', { count: list.length }) : undefined}
        actions={
          category &&
          canEditProducts && (
            <>
              {!category.parentId && (
                <Button icon={<FolderPlus size={16} />} onClick={() => setCatEdit({ category: null, parentId: category.id })}>
                  {t('admin.products.category.addSub')}
                </Button>
              )}
              <Button icon={<Pencil size={16} />} onClick={() => setCatEdit({ category })}>
                {t('admin.products.category.edit')}
              </Button>
              <Button variant="primary" icon={<Plus size={16} />} onClick={() => setEditing('new')}>
                {t('admin.products.add')}
              </Button>
            </>
          )
        }
      />
      <ErrorText error={archive.error} />
      {notFound ? (
        <div className="state state-empty">
          <div>{t('admin.products.category.notFoundText')}</div>
          <Link to="/products" className="btn btn-secondary">
            {t('admin.products.backToOverview')}
          </Link>
        </div>
      ) : (
        <DataState loading={(cats.loading && !cats.data) || (prods.loading && !prods.data)} error={cats.error ?? prods.error} onRetry={() => (cats.reload(), prods.reload())}>
          {category && <TypeFilterBar products={list} value={typeFilter} onChange={setTypeFilter} />}
          {category && nothingOfType && <div className="state state-empty">{t('admin.products.filter.none')}</div>}
          {category && !nothingOfType && (
            <div className="product-sections">
              {(filtering ? ownShown.length > 0 : own.length > 0 || children.length === 0) && (
                <section className="product-section">
                  {children.length > 0 && <h2 className="section-title">{t('admin.products.directlyIn', { name: category.name })}</h2>}
                  {own.length === 0 ? (
                    <div className="state state-empty">
                      <div>{t('admin.products.emptyCategory')}</div>
                      {canEditProducts && (
                        <Button variant="primary" icon={<Plus size={16} />} onClick={() => setEditing('new')}>
                          {t('admin.products.add')}
                        </Button>
                      )}
                    </div>
                  ) : (
                    <ProductTable products={ownShown} h={handlers} onReorder={sortIn(category.id)} />
                  )}
                </section>
              )}
              {childSections.map(({ ch, items }) => {
                if (filtering && items.length === 0) return null;
                return (
                  <section key={ch.id} className="product-section">
                    <h2 className="section-title">
                      <Link to={`/products/category/${ch.id}`}>{ch.name}</Link>
                      <span className="faint"> · {t('admin.products.count', { count: items.length })}</span>
                    </h2>
                    {items.length === 0 ? (
                      <div className="state state-empty state-compact">{t('admin.products.emptyCategory')}</div>
                    ) : (
                      <ProductTable products={items} h={handlers} onReorder={sortIn(ch.id)} />
                    )}
                  </section>
                );
              })}
            </div>
          )}
        </DataState>
      )}
      {editing && (
        <ProductDialog
          product={editing === 'new' || 'copyOf' in editing ? null : editing}
          copyOf={editing !== 'new' && 'copyOf' in editing ? editing.copyOf : undefined}
          categoryId={category?.id}
          categories={all}
          onClose={closeEditor}
          onSaved={() => {
            setEditing(null);
            prods.reload();
            cats.reload();
          }}
        />
      )}
      {catEdit && (
        <CategoryDialog
          category={catEdit.category}
          parentId={catEdit.parentId}
          categories={all}
          onClose={closeCat}
          onSaved={() => {
            setCatEdit(null);
            cats.reload();
            prods.reload();
          }}
        />
      )}
    </div>
  );
}
