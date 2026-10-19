// Variant list of the product dialog: one input per variant, drag handle to sort, Enter adds
// the next variant, a pasted list ("Orange, Apple, Guava") becomes several variants.
import { useRef, useState } from 'react';
import { GripVertical, ListPlus, Plus, Trash2 } from 'lucide-react';
import { useT } from '../../lib/hooks';
import { Button, IconButton } from '../../components/ui';
import { useSortable } from '../../components/sortable';
import { duplicateNames, splitVariantNames, type VariantRow } from './variants';

export const MAX_VARIANTS = 50;

export function VariantsEditor({
  rows,
  onChange,
  description,
  onTakeDescription,
}: {
  rows: VariantRow[];
  onChange: (rows: VariantRow[]) => void;
  /** The product description: offered as variants while there are none yet. */
  description: string;
  /** The description was taken over as variants (the dialog clears it). */
  onTakeDescription: () => void;
}) {
  const t = useT();
  const nextKey = useRef(rows.reduce((m, r) => Math.max(m, r.key), 0) + 1);
  const [focusKey, setFocusKey] = useState<number | null>(null);
  const sort = useSortable(
    rows.map((r) => String(r.key)),
    (keys) => onChange(keys.map((k) => rows.find((r) => String(r.key) === k)!)),
  );
  const dup = duplicateNames(rows);
  const fromDescription = rows.length === 0 ? splitVariantNames(description) : [];

  const insert = (at: number, names: string[], base: VariantRow[] = rows): void => {
    const added = names.map((name) => ({ key: nextKey.current++, name }));
    onChange([...base.slice(0, at), ...added, ...base.slice(at)].slice(0, MAX_VARIANTS));
    setFocusKey(added.at(-1)?.key ?? null);
  };
  const rename = (key: number, name: string): void => onChange(rows.map((r) => (r.key === key ? { ...r, name } : r)));

  return (
    <div className="variants">
      {rows.length > 0 && (
        <ul className="variant-list">
          {rows.map((r, i) => {
            const isDup = dup.has(r.name.trim().toLowerCase());
            return (
              <li key={r.key} {...sort.itemProps(String(r.key))}>
                <button type="button" {...sort.handleProps(String(r.key), t('admin.products.dragToSort'))}>
                  <GripVertical size={16} />
                </button>
                <input
                  className={`input ${isDup ? 'invalid' : ''}`}
                  value={r.name}
                  maxLength={100}
                  autoFocus={r.key === focusKey}
                  aria-label={t('admin.products.variants.name', { n: i + 1 })}
                  aria-invalid={isDup || undefined}
                  title={isDup ? t('admin.products.variants.duplicate') : undefined}
                  placeholder={t('admin.products.variants.placeholder')}
                  onChange={(e) => rename(r.key, e.target.value)}
                  onKeyDown={(e) => {
                    // Enter adds the next variant instead of submitting the dialog.
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      if (r.name.trim() && rows.length < MAX_VARIANTS) insert(i + 1, ['']);
                    }
                  }}
                  onPaste={(e) => {
                    const names = splitVariantNames(e.clipboardData.getData('text'));
                    if (names.length < 2) return;
                    e.preventDefault();
                    if (r.name.trim()) insert(i + 1, names);
                    else insert(i + 1, names.slice(1), rows.map((x) => (x.key === r.key ? { ...x, name: names[0]! } : x)));
                  }}
                />
                <IconButton label={t('admin.products.variants.remove', { name: r.name || '…' })} onClick={() => onChange(rows.filter((x) => x.key !== r.key))}>
                  <Trash2 size={16} />
                </IconButton>
              </li>
            );
          })}
        </ul>
      )}
      <div className="variant-actions">
        <Button icon={<Plus size={16} />} disabled={rows.length >= MAX_VARIANTS} onClick={() => insert(rows.length, [''])}>
          {t('admin.products.variants.add')}
        </Button>
        {fromDescription.length > 1 && (
          <Button
            variant="ghost"
            icon={<ListPlus size={16} />}
            onClick={() => {
              insert(0, fromDescription);
              onTakeDescription();
            }}
          >
            {t('admin.products.variants.fromDescription', { count: fromDescription.length })}
          </Button>
        )}
      </div>
    </div>
  );
}
