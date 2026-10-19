// Add/edit a product in a dialog (no inline forms). Prices are typed as
// decimals and sent as integer minor units.
import { useCallback, useMemo, useState } from 'react';
import { ImagePlus, Trash2 } from 'lucide-react';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { patch, post } from '../../lib/api';
import { useAction, useT } from '../../lib/hooks';
import { useSession } from '../../lib/session';
import { useToast } from '../../components/feedback';
import { Button, Checkbox, Dialog, ErrorText, Field, Select, TextInput } from '../../components/ui';
import { Segmented } from '../../components/controls';
import { MediaPickerDialog, MediaThumb } from '../../components/MediaPicker';
import { currencyDigits, decimalSeparator, formatMoneyInput, formatQuantityValue, parseMoney, parseQuantity, validCurrency } from './money';
import { categoryOptions, PRODUCT_TYPES, QUANTITY_UNITS, type Category, type Product, type ProductType } from './types';
import { duplicateNames, variantsBody, type VariantRow } from './variants';
import { VariantsEditor } from './VariantsEditor';

/** Edit a product, create one, or create a copy (copyOf: all fields taken over, saved as a
 *  new product right after the original). */
export function ProductDialog({
  product,
  copyOf,
  categoryId,
  categories,
  onClose,
  onSaved,
}: {
  product: Product | null;
  copyOf?: Product;
  categoryId?: string;
  categories: Category[];
  onClose: () => void;
  onSaved: (p: Product) => void;
}) {
  const t = useT();
  const toast = useToast();
  const { server } = useSession();
  const lang = getLanguage();
  const sep = decimalSeparator(lang);
  const src = product ?? copyOf ?? null;
  const initialCurrency = src?.price.currency ?? server?.currency ?? 'EUR';
  const [form, setForm] = useState(() => ({
    name: src?.name ?? '',
    description: src?.description ?? '',
    categoryId: src?.categoryId ?? categoryId ?? categories[0]?.id ?? '',
    type: (src?.type ?? 'drink') as ProductType,
    quantity: src?.quantity ? formatQuantityValue(src.quantity.value, sep) : '',
    unit: src?.quantity?.unit ?? 'ml',
    price: src ? formatMoneyInput(src.price.amount, currencyDigits(src.price.currency), sep) : '',
    currency: initialCurrency,
    imageAssetId: src?.imageAssetId ?? '',
    imageName: '',
    showImage: src?.showImageInsteadOfName ?? false,
    imageScale: String(src?.imageScale ?? 100),
  }));
  // A copy gets new variants with the same names (and every variant available).
  const [variants, setVariants] = useState<VariantRow[]>(() => (src?.variants ?? []).map((v, i) => ({ key: i + 1, id: product ? v.id : undefined, name: v.name })));
  const [picking, setPicking] = useState(false);
  const set = <K extends keyof typeof form>(k: K, v: (typeof form)[K]): void => setForm((f) => ({ ...f, [k]: v }));

  const currency = form.currency.trim().toUpperCase();
  const currencyOk = validCurrency(currency);
  const amount = useMemo(() => (form.price.trim() ? parseMoney(form.price, currencyDigits(currencyOk ? currency : 'EUR')) : null), [form.price, currency, currencyOk]);
  const qty = form.quantity.trim() ? parseQuantity(form.quantity) : null;
  const qtyBad = form.quantity.trim() !== '' && qty === null;
  const priceBad = form.price.trim() !== '' && amount === null;
  // Logo size in percent of the automatic size (letters as high as the product font).
  // Only checked while the field is shown; otherwise the stored size stays.
  const scaleShown = !!form.imageAssetId && form.showImage;
  const typed = /^\d{1,3}$/.test(form.imageScale.trim()) ? Number(form.imageScale.trim()) : NaN;
  const scaleBad = scaleShown && !(typed >= 25 && typed <= 400);
  const scale = scaleShown && !scaleBad ? typed : (src?.imageScale ?? 100);
  const valid = form.name.trim() !== '' && form.categoryId !== '' && amount !== null && currencyOk && !qtyBad && !scaleBad && duplicateNames(variants).size === 0;

  const save = useAction(async () => {
    if (amount === null) return;
    const body = {
      name: form.name.trim(),
      description: form.description.trim(),
      categoryId: form.categoryId,
      type: form.type,
      quantity: qty ? { value: qty, unit: form.unit } : null,
      price: { amount, currency },
      imageAssetId: form.imageAssetId || null,
      showImageInsteadOfName: !!form.imageAssetId && form.showImage,
      imageScale: scale,
      variants: variantsBody(variants),
    };
    const saved = product
      ? await patch<Product>(`/api/v1/products/${product.id}`, { ...body, revision: product.revision })
      : await post<Product>('/api/v1/products', copyOf ? { ...body, afterId: copyOf.id } : body);
    toast(t('admin.saved'));
    onSaved(saved);
  });

  const closePicker = useCallback(() => setPicking(false), []);

  return (
    <Dialog
      title={product ? t('admin.products.edit') : copyOf ? t('admin.products.duplicateTitle') : t('admin.products.add')}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" type="submit" form="product-form" busy={save.busy} disabled={!valid}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <form
        id="product-form"
        className="product-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) void save.run();
        }}
      >
        <div className="form">
          <TextInput label={t('admin.products.field.name')} value={form.name} maxLength={200} onChange={(e) => set('name', e.target.value)} />
          <Field label={t('admin.products.field.description')} htmlFor="product-description">
            <textarea id="product-description" className="input" rows={3} maxLength={2000} value={form.description} onChange={(e) => set('description', e.target.value)} />
          </Field>
          <Field label={t('admin.products.field.variants')} hint={t('admin.products.variants.hint')} error={duplicateNames(variants).size > 0 ? t('admin.products.variants.duplicate') : null}>
            <VariantsEditor rows={variants} onChange={setVariants} description={form.description} onTakeDescription={() => set('description', '')} />
          </Field>
          <Select label={t('admin.products.field.category')} value={form.categoryId} options={categoryOptions(categories)} onChange={(e) => set('categoryId', e.target.value)} />
          <Field label={t('admin.products.field.type')}>
            <div>
              <Segmented value={form.type} label={t('admin.products.field.type')} onChange={(v) => set('type', v)} options={PRODUCT_TYPES.map((p) => ({ value: p, label: t(`admin.products.type.${p}`) }))} />
            </div>
          </Field>
          <div className="form-row">
            <TextInput
              label={t('admin.products.field.quantity')}
              inputMode="decimal"
              value={form.quantity}
              placeholder={`0${sep}33`}
              onChange={(e) => set('quantity', e.target.value)}
              error={qtyBad ? t('admin.products.quantityInvalid') : null}
              hint={t('admin.products.quantityHint')}
            />
            <Select label={t('admin.products.field.unit')} value={form.unit} onChange={(e) => set('unit', e.target.value)} options={QUANTITY_UNITS.map((u) => ({ value: u, label: t(`admin.products.unit.${u}`) }))} />
          </div>
          <div className="form-row">
            <TextInput
              label={t('admin.products.field.price')}
              inputMode="decimal"
              value={form.price}
              placeholder={`4${sep}50`}
              onChange={(e) => set('price', e.target.value)}
              error={priceBad ? t('admin.products.priceInvalid') : null}
              hint={amount !== null && currencyOk ? formatMoney(amount, currency, lang) : undefined}
            />
            <TextInput
              label={t('admin.products.field.currency')}
              value={form.currency}
              maxLength={3}
              className="input currency-input"
              onChange={(e) => set('currency', e.target.value.toUpperCase())}
              error={currencyOk ? null : t('admin.products.currencyInvalid')}
            />
          </div>
        </div>
        <div className="form product-form-side">
          <Field label={t('admin.products.field.image')}>
            <div className="product-image-box">
              {form.imageAssetId ? <MediaThumb assetId={form.imageAssetId} alt={form.imageName} /> : <div className="product-image-empty muted">{t('admin.products.noImage')}</div>}
            </div>
          </Field>
          <div className="product-image-actions">
            <Button icon={<ImagePlus size={16} />} onClick={() => setPicking(true)}>
              {form.imageAssetId ? t('admin.products.changeImage') : t('admin.products.chooseImage')}
            </Button>
            {form.imageAssetId && (
              <Button variant="ghost" icon={<Trash2 size={16} />} onClick={() => setForm((f) => ({ ...f, imageAssetId: '', imageName: '', showImage: false }))}>
                {t('admin.products.removeImage')}
              </Button>
            )}
          </div>
          <Checkbox label={t('admin.products.field.showImage')} checked={!!form.imageAssetId && form.showImage} disabled={!form.imageAssetId} onChange={(v) => set('showImage', v)} />
          <div className="field-hint">{t('admin.products.showImageHint')}</div>
          {scaleShown && (
            <TextInput
              label={t('admin.products.field.imageScale')}
              type="number"
              inputMode="numeric"
              min={25}
              max={400}
              step={5}
              value={form.imageScale}
              onChange={(e) => set('imageScale', e.target.value)}
              error={scaleBad ? t('admin.products.imageScaleInvalid') : null}
              hint={t('admin.products.imageScaleHint')}
            />
          )}
        </div>
        <div className="product-form-error">
          <ErrorText error={save.error} />
        </div>
      </form>
      {picking && (
        <MediaPickerDialog
          kinds={['image']}
          title={t('admin.products.chooseImage')}
          onClose={closePicker}
          onPick={(a) => {
            setForm((f) => ({ ...f, imageAssetId: a.id, imageName: a.name }));
            setPicking(false);
          }}
        />
      )}
    </Dialog>
  );
}
