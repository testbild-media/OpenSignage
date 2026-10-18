// Product archive: restore or delete permanently (only archived products can be deleted;
// the server refuses while schedule prices or presentations still use the product).
import { Link } from 'react-router';
import { ArchiveRestore, ChevronRight, Trash2 } from 'lucide-react';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { ApiError, del, errorText, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { DataState, IconButton, PageHeader } from '../../components/ui';
import { useRights } from '../../lib/rights';
import { MediaThumb } from '../../components/MediaPicker';
import { categoryPath, type Category, type Product } from './types';

/** Explains product_in_use with the names the server sent. */
export function productInUseText(e: unknown, t: (k: string, p?: Record<string, string | number>) => string): string {
  if (!(e instanceof ApiError) || e.code !== 'product_in_use') return errorText(e);
  const list = (v: unknown): string => (Array.isArray(v) ? v.map(String).join(', ') : '');
  const parts = [t('errors.product_in_use')];
  const rules = list(e.params.rules);
  const pres = list(e.params.presentations);
  if (rules) parts.push(t('admin.products.inUse.rules', { names: rules }));
  if (pres) parts.push(t('admin.products.inUse.presentations', { names: pres }));
  return parts.join(' ');
}

export function ArchivePage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const lang = getLanguage();
  const edit = useRights().canEditProducts;
  const q = useQuery<{ products: Product[] }>('/api/v1/products?archived=1');
  const cats = useQuery<{ categories: Category[] }>('/api/v1/categories');
  useEvents(['products'], () => {
    q.reload();
    cats.reload();
  });
  const restore = useAction(async (p: Product) => {
    const ok = await confirm({ title: t('admin.products.archive.restore'), message: t('admin.products.archive.restoreConfirm', { name: p.name }), confirmLabel: t('admin.products.archive.restore') });
    if (!ok) return;
    await post(`/api/v1/products/${p.id}/restore`);
    toast(t('admin.products.archive.restored', { name: p.name }));
    q.reload();
  });
  const remove = useAction(async (p: Product) => {
    const ok = await confirm({
      title: t('admin.products.archive.delete'),
      message: t('admin.products.archive.deleteConfirm', { name: p.name }),
      confirmLabel: t('admin.products.archive.delete'),
      danger: true,
    });
    if (!ok) return;
    try {
      await del(`/api/v1/products/${p.id}`);
    } catch (e) {
      toast(productInUseText(e, t), 'error');
      return;
    }
    toast(t('admin.products.archive.deleted', { name: p.name }));
    q.reload();
  });
  const busy = restore.busy || remove.busy;
  const all = cats.data?.categories ?? [];
  const items = q.data?.products ?? [];
  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/products">{t('admin.products.title')}</Link>
        <ChevronRight size={14} />
      </div>
      <PageHeader title={t('admin.products.archive.title')} subtitle={t('admin.products.archive.subtitle')} />
      {restore.error != null && <div className="form-error">{errorText(restore.error)}</div>}
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={items.length === 0} emptyText={t('admin.products.archive.empty')}>
        <table className="table product-table">
          <thead>
            <tr>
              <th>{t('admin.products.field.name')}</th>
              <th>{t('admin.products.field.category')}</th>
              <th className="num">{t('admin.products.field.price')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {items.map((p) => (
              <tr key={p.id}>
                <td>
                  <div className="product-cell">
                    {p.imageAssetId ? <MediaThumb assetId={p.imageAssetId} className="product-thumb" /> : <span className="product-thumb product-thumb-empty" />}
                    <div className="product-text">
                      <div className="product-name">
                        <span className="ellipsis">{p.name}</span>
                      </div>
                      {p.description && <div className="product-desc">{p.description}</div>}
                    </div>
                  </div>
                </td>
                <td className="muted">{categoryPath(all, p.categoryId) || '—'}</td>
                <td className="nowrap num">{formatMoney(p.price.amount, p.price.currency, lang)}</td>
                <td className="row-actions">
                  {edit && (
                    <>
                      <IconButton label={t('admin.products.archive.restore')} disabled={busy} onClick={() => void restore.run(p)}>
                        <ArchiveRestore size={16} />
                      </IconButton>
                      <IconButton label={t('admin.products.archive.delete')} disabled={busy} onClick={() => void remove.run(p)}>
                        <Trash2 size={16} />
                      </IconButton>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
    </div>
  );
}
