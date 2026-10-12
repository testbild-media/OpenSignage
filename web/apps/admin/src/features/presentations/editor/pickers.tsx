// Pickers used by the editor: media (the shared folder browser) and product/category sources.
import { useState } from 'react';
import { useQuery, useT } from '../../../lib/hooks';
import { Button, DataState } from '../../../components/ui';
import { MediaPickerDialog as SharedMediaPicker, mediaThumbUrl } from '../../../components/MediaPicker';

const TITLE = { image: 'media.pick.image', video: 'media.pick.video', audio: 'media.pick.audio' } as const;

/** Media chooser with real folder navigation (never a flat indented list); a click picks. */
export function MediaPickerDialog({ kind, onPick, onClose }: { kind: 'image' | 'video' | 'audio'; onPick: (id: string) => void; onClose: () => void }) {
  const t = useT();
  return <SharedMediaPicker kinds={[kind]} title={t(TITLE[kind])} pickOnClick onPick={(a) => onPick(a.id)} onClose={onClose} />;
}

export function MediaPickerButton({ kind, value, onChange }: { kind: 'image' | 'video' | 'audio'; value: string; onChange: (id: string) => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <>
      <span className="media-pick">
        {value && kind !== 'audio' && <img src={mediaThumbUrl(value)} alt="" />}
        <Button className="btn-sm" onClick={() => setOpen(true)}>
          {value ? t('editor.pick.change') : t('editor.pick.choose')}
        </Button>
      </span>
      {open && (
        <MediaPickerDialog
          kind={kind}
          onClose={() => setOpen(false)}
          onPick={(id) => {
            onChange(id);
            setOpen(false);
          }}
        />
      )}
    </>
  );
}

interface Category {
  id: string;
  parentId?: string | null;
  name: string;
}
interface Product {
  id: string;
  name: string;
  categoryId: string;
}

/** Selects categories or products as the source of a product list. */
export function SourcePicker({ kind, ids, onChange, single }: { kind: 'categories' | 'products'; ids: string[]; onChange: (ids: string[]) => void; single?: boolean }) {
  const t = useT();
  const cats = useQuery<{ categories: Category[] }>('/api/v1/categories');
  const prods = useQuery<{ products: Product[] }>(kind === 'products' ? '/api/v1/products?archived=0' : null);
  const toggle = (id: string): void => {
    if (single) onChange([id]);
    else onChange(ids.includes(id) ? ids.filter((x) => x !== id) : [...ids, id]);
  };
  const catName = (id: string): string => cats.data?.categories.find((c) => c.id === id)?.name ?? '';
  return (
    <div className="source-picker">
      <DataState loading={(cats.loading && !cats.data) || (kind === 'products' && prods.loading && !prods.data)} error={cats.error ?? prods.error}>
        {kind === 'categories' &&
          cats.data?.categories.map((c) => (
            <label key={c.id} className={`checkbox small ${c.parentId ? 'indent' : ''}`}>
              <input type="checkbox" checked={ids.includes(c.id)} onChange={() => toggle(c.id)} />
              <span>{c.name}</span>
            </label>
          ))}
        {kind === 'products' &&
          prods.data?.products.map((p) => (
            <label key={p.id} className="checkbox small">
              <input type={single ? 'radio' : 'checkbox'} checked={ids.includes(p.id)} onChange={() => toggle(p.id)} />
              <span>
                {p.name} <em className="faint">{catName(p.categoryId)}</em>
              </span>
            </label>
          ))}
        {kind === 'products' && prods.data?.products.length === 0 && <div className="faint">{t('editor.pick.noProducts')}</div>}
      </DataState>
    </div>
  );
}
