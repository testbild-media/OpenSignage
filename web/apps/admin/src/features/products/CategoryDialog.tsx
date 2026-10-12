import { useState } from 'react';
import { patch, post } from '../../lib/api';
import { useAction, useT } from '../../lib/hooks';
import { useToast } from '../../components/feedback';
import { Button, Dialog, ErrorText, Select, TextInput } from '../../components/ui';
import type { Category } from './types';

/** Create or rename a category; a category can also be moved under another top-level one. */
export function CategoryDialog({
  category,
  parentId,
  categories,
  onClose,
  onSaved,
}: {
  category: Category | null;
  /** Preset parent for a new subcategory. */
  parentId?: string;
  categories: Category[];
  onClose: () => void;
  onSaved: (c: Category) => void;
}) {
  const t = useT();
  const toast = useToast();
  const [name, setName] = useState(category?.name ?? '');
  const [parent, setParent] = useState(category?.parentId ?? parentId ?? '');
  const hasChildren = !!category && categories.some((c) => c.parentId === category.id);
  const parents = categories.filter((c) => !c.parentId && c.id !== category?.id);
  const save = useAction(async () => {
    const body = { name: name.trim(), parentId: parent || null };
    const saved = category
      ? await patch<Category>(`/api/v1/categories/${category.id}`, { ...body, revision: category.revision })
      : await post<Category>('/api/v1/categories', body);
    toast(t('admin.saved'));
    onSaved(saved);
  });
  const title = category ? t('admin.products.category.edit') : parent ? t('admin.products.category.addSub') : t('admin.products.category.add');
  return (
    <Dialog
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" type="submit" form="category-form" busy={save.busy} disabled={!name.trim()}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <form
        id="category-form"
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) void save.run();
        }}
      >
        <TextInput label={t('admin.products.category.name')} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
        <Select
          label={t('admin.products.category.parent')}
          value={parent}
          disabled={hasChildren}
          hint={hasChildren ? t('admin.products.category.parentLocked') : t('admin.products.category.parentHint')}
          onChange={(e) => setParent(e.target.value)}
          options={[{ value: '', label: t('admin.products.category.topLevel') }, ...parents.map((c) => ({ value: c.id, label: c.name }))]}
        />
        <ErrorText error={save.error} />
      </form>
    </Dialog>
  );
}
