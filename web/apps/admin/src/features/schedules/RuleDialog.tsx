// Editor for a time schedule rule: scope, zone, weekly window (with a clear explanation of
// windows over midnight) and the list of things it changes: the playlist of
// all screens, the playlist of single display groups, price overrides, showing a ticker over
// the zone's screens (with a preview in its template) or hiding it; enabled.
import { useMemo, useRef, useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { NO_GROUP_ID, type GroupPlaylist, type PriceOverride, type Rule, type TickerTemplate } from '@opensignage/contracts';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { patch, post } from '../../lib/api';
import { useAction, useT } from '../../lib/hooks';
import { useToast } from '../../components/feedback';
import { Button, Checkbox, Dialog, ErrorText, Field, IconButton, Select, TextInput } from '../../components/ui';
import { Segmented } from '../../components/controls';
import { currencyDigits, decimalSeparator, formatMoneyInput, parseMoney } from '../products/money';
import { categoryTree, type Category, type Product } from '../products/types';
import { playlistOptions, type PlaylistSummary, type TickerTemplateRecord, type Zone } from '../zones/types';
import { TickerPreview } from '../zones/TickerPreview';
import { midnightInfo, parseHHMM, weekdayLabels } from './windows';

type Scope = Rule['scope'];
type TickerMode = 'keep' | 'show' | 'hide';

interface PriceRow {
  key: number;
  productId: string;
  price: string;
}

export interface RuleDialogData {
  /** Zones the user may write rules for. */
  zones: Zone[];
  /** Global rules (admins and operators of all zones). */
  globalAllowed: boolean;
  playlists: PlaylistSummary[] | undefined;
  products: Product[];
  archivedProducts: Product[];
  categories: Category[];
  tickerTemplates: TickerTemplateRecord[];
  defaultTicker: TickerTemplate | undefined;
}

function ProductSelect({ value, products, categories, used, onChange, label, missingLabel }: { value: string; products: Product[]; categories: Category[]; used: Set<string>; onChange: (id: string) => void; label: string; missingLabel?: string }) {
  const groups = useMemo(() => {
    const tree = categoryTree(categories);
    const order = tree.flatMap((c) => [c, ...c.children]);
    return order
      .map((c) => ({
        id: c.id,
        label: c.parentId ? `${tree.find((x) => x.id === c.parentId)?.name ?? ''} › ${c.name}` : c.name,
        items: products.filter((p) => p.categoryId === c.id),
      }))
      .filter((g) => g.items.length > 0);
  }, [products, categories]);
  return (
    <select className="input" aria-label={label} value={value} onChange={(e) => onChange(e.target.value)}>
      {missingLabel && <option value={value}>{missingLabel}</option>}
      {groups.map((g) => (
        <optgroup key={g.id} label={g.label}>
          {g.items.map((p) => (
            <option key={p.id} value={p.id} disabled={p.id !== value && used.has(p.id)}>
              {p.name}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  );
}

export function RuleDialog({
  rule,
  preset,
  data,
  onClose,
  onSaved,
}: {
  rule: Rule | null;
  preset?: { scope: Scope; zoneId?: string };
  data: RuleDialogData;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const lang = getLanguage();
  const sep = decimalSeparator(lang);
  const labels = useMemo(() => weekdayLabels(lang), [lang]);
  const longLabels = useMemo(() => weekdayLabels(lang, 'long'), [lang]);
  const productById = useMemo(() => new Map([...data.archivedProducts, ...data.products].map((p) => [p.id, p])), [data.products, data.archivedProducts]);
  const nextKey = useRef(1);

  const [name, setName] = useState(rule?.name ?? '');
  const [scope, setScope] = useState<Scope>(rule?.scope ?? preset?.scope ?? 'zone');
  const [zoneId, setZoneId] = useState(rule?.zoneId ?? preset?.zoneId ?? data.zones[0]?.id ?? '');
  const [days, setDays] = useState<number[]>(rule?.window?.days ?? [1, 2, 3, 4, 5]);
  const [start, setStart] = useState(rule?.window?.start ?? '17:00');
  const [end, setEnd] = useState(rule?.window?.end ?? '19:00');
  const [playlistId, setPlaylistId] = useState(rule?.playlistId ?? '');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [tickerMode, setTickerMode] = useState<TickerMode>(rule?.ticker ? (rule.ticker.hide ? 'hide' : 'show') : 'keep');
  const tickerOn = tickerMode === 'show';
  const [groupPls, setGroupPls] = useState<Record<string, string>>(() => Object.fromEntries((rule?.groupPlaylists ?? []).map((g) => [g.groupId, g.playlistId])));
  const [tickerText, setTickerText] = useState(rule?.ticker?.text ?? '');
  const [tickerTemplateId, setTickerTemplateId] = useState(rule?.ticker?.templateId ?? '');
  const [prices, setPrices] = useState<PriceRow[]>(() =>
    (rule?.prices ?? []).map((p) => ({ key: nextKey.current++, productId: p.productId, price: formatMoneyInput(p.price.amount, currencyDigits(p.price.currency), sep) })),
  );

  const windowed = scope !== 'default';
  // Display groups of the zone plus the screens without group.
  const zoneGroups = scope === 'global' ? [] : (data.zones.find((z) => z.id === zoneId)?.groups ?? []);
  const groupTargets = zoneGroups.length ? [...zoneGroups.map((g) => ({ id: g.id, name: g.name })), { id: NO_GROUP_ID, name: t('admin.zones.groups.ungrouped') }] : [];
  const groupPlaylists: GroupPlaylist[] = groupTargets.filter((g) => groupPls[g.id]).map((g) => ({ groupId: g.id, playlistId: groupPls[g.id]! }));
  const info = midnightInfo(start, end);
  const used = new Set(prices.map((p) => p.productId));
  const parsed = prices.map((row) => {
    const prod = productById.get(row.productId);
    const currency = prod?.price.currency ?? 'EUR';
    const amount = row.price.trim() ? parseMoney(row.price, currencyDigits(currency)) : null;
    return { row, prod, currency, amount };
  });
  const pricesOk = parsed.every((p) => p.amount !== null && !!p.row.productId);
  const problems: string[] = [];
  if (!name.trim()) problems.push(t('admin.schedules.problem.name'));
  if (scope !== 'global' && !zoneId) problems.push(t('admin.schedules.problem.zone'));
  if (windowed && days.length === 0) problems.push(t('admin.schedules.problem.days'));
  if (windowed && (parseHHMM(start) === null || parseHHMM(end) === null)) problems.push(t('admin.schedules.problem.time'));
  if (!playlistId && groupPlaylists.length === 0 && prices.length === 0 && tickerMode === 'keep') problems.push(t('admin.schedules.problem.content'));
  if (tickerOn && !tickerText.trim()) problems.push(t('admin.schedules.problem.tickerText'));
  if (!pricesOk) problems.push(t('admin.schedules.problem.prices'));

  const save = useAction(async () => {
    const body = {
      name: name.trim(),
      scope,
      zoneId: scope === 'global' ? null : zoneId,
      window: windowed ? { days: [...days].sort((a, b) => a - b), start, end } : null,
      playlistId: playlistId || null,
      groupPlaylists,
      prices: parsed.map((p): PriceOverride => ({ productId: p.row.productId, price: { amount: p.amount ?? 0, currency: p.currency } })),
      ticker: tickerMode === 'show' ? { text: tickerText.trim(), templateId: tickerTemplateId } : tickerMode === 'hide' ? { hide: true, text: '' } : null,
      enabled,
    };
    if (rule) await patch(`/api/v1/schedules/${rule.id}`, { ...body, revision: rule.revision });
    else await post('/api/v1/schedules', body);
    toast(t('admin.saved'));
    onSaved();
  });

  // The style the screens will use: the rule's template, else the zone's, else the default.
  const zoneTemplateId = scope === 'global' ? '' : (data.zones.find((z) => z.id === zoneId)?.tickerTemplateId ?? '');
  const previewTemplate =
    data.tickerTemplates.find((x) => x.id === (tickerTemplateId || zoneTemplateId))?.template ?? data.defaultTicker;

  const toggleDay = (d: number): void => setDays((ds) => (ds.includes(d) ? ds.filter((x) => x !== d) : [...ds, d]));
  const addPrice = (): void => {
    const first = data.products.find((p) => !used.has(p.id));
    if (!first) return;
    setPrices((ps) => [...ps, { key: nextKey.current++, productId: first.id, price: formatMoneyInput(first.price.amount, currencyDigits(first.price.currency), sep) }]);
  };

  return (
    <Dialog
      title={rule ? t('admin.schedules.edit') : t('admin.schedules.add')}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" type="submit" form="rule-form" busy={save.busy} disabled={problems.length > 0}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <form
        id="rule-form"
        className="rule-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (problems.length === 0) void save.run();
        }}
      >
        <div className="form">
          <TextInput label={t('admin.schedules.field.name')} value={name} maxLength={200} placeholder={t('admin.schedules.namePlaceholder')} onChange={(e) => setName(e.target.value)} />
          <Field label={t('admin.schedules.field.scope')} hint={t(`admin.schedules.scopeHint.${scope}`)}>
            <div>
              <Segmented
                value={scope}
                label={t('admin.schedules.field.scope')}
                onChange={setScope}
                options={(['zone', 'global', 'default'] as Scope[]).filter((s) => s !== 'global' || data.globalAllowed || scope === 'global').map((s) => ({ value: s, label: t(`admin.schedules.scope.${s}`) }))}
              />
            </div>
          </Field>
          {scope !== 'global' && (
            <Select
              label={t('admin.schedules.field.zone')}
              value={zoneId}
              onChange={(e) => setZoneId(e.target.value)}
              options={[...(zoneId ? [] : [{ value: '', label: '—' }]), ...data.zones.map((z) => ({ value: z.id, label: z.name }))]}
            />
          )}
          {windowed && (
            <>
              <Field label={t('admin.schedules.field.days')}>
                <div className="day-toggles" role="group" aria-label={t('admin.schedules.field.days')}>
                  {[1, 2, 3, 4, 5, 6, 7].map((d) => (
                    <button key={d} type="button" className={days.includes(d) ? 'active' : ''} aria-pressed={days.includes(d)} title={longLabels[d]} onClick={() => toggleDay(d)}>
                      {labels[d]}
                    </button>
                  ))}
                  <span className="day-presets">
                    <button type="button" className="link-btn" onClick={() => setDays([1, 2, 3, 4, 5])}>
                      {t('admin.schedules.preset.weekdays')}
                    </button>
                    <button type="button" className="link-btn" onClick={() => setDays([6, 7])}>
                      {t('admin.schedules.preset.weekend')}
                    </button>
                    <button type="button" className="link-btn" onClick={() => setDays([1, 2, 3, 4, 5, 6, 7])}>
                      {t('admin.schedules.preset.daily')}
                    </button>
                  </span>
                </div>
              </Field>
              <div className="form-row">
                <TextInput label={t('admin.schedules.field.start')} type="time" step={60} value={start} onChange={(e) => setStart(e.target.value.slice(0, 5))} />
                <TextInput label={t('admin.schedules.field.end')} type="time" step={60} value={end} onChange={(e) => setEnd(e.target.value.slice(0, 5))} />
              </div>
              {info && (
                <div className={`midnight-hint ${info === 'same_day' ? '' : 'emphasis'}`}>
                  {t(`admin.schedules.midnight.${info}`, { start, end })}
                  {info !== 'same_day' && <div className="faint">{t('admin.schedules.midnight.daysNote')}</div>}
                </div>
              )}
            </>
          )}
          <Select
            label={groupTargets.length ? t('admin.schedules.field.playlistAll') : t('admin.schedules.field.playlist')}
            value={playlistId}
            hint={playlistId ? t('admin.schedules.playlistHint') : t('admin.schedules.pricesOnlyHint')}
            onChange={(e) => setPlaylistId(e.target.value)}
            options={playlistOptions(data.playlists, playlistId, t('admin.schedules.pricesOnlyOption'), (n) => t('admin.zones.notPublished', { name: n }))}
          />
          {groupTargets.length > 0 && (
            <Field label={t('admin.schedules.groups.title')} hint={t('admin.schedules.groups.hint')}>
              <div className="rule-groups">
                {groupTargets.map((g) => (
                  <label key={g.id} className="rule-group-row">
                    <span>{g.name}</span>
                    <select className="input" value={groupPls[g.id] ?? ''} onChange={(e) => setGroupPls((m) => ({ ...m, [g.id]: e.target.value }))}>
                      {playlistOptions(data.playlists, groupPls[g.id], t('admin.schedules.groups.keep'), (n) => t('admin.zones.notPublished', { name: n })).map((o) => (
                        <option key={o.value} value={o.value}>
                          {o.label}
                        </option>
                      ))}
                    </select>
                  </label>
                ))}
              </div>
            </Field>
          )}
          <Checkbox label={t('admin.schedules.field.enabled')} checked={enabled} onChange={setEnabled} />
        </div>
        <div className="form rule-prices">
          <div className="rule-prices-head">
            <h3>{t('admin.schedules.prices.title')}</h3>
            <Button icon={<Plus size={16} />} disabled={data.products.every((p) => used.has(p.id))} onClick={addPrice}>
              {t('admin.schedules.prices.add')}
            </Button>
          </div>
          <p className="faint">{t('admin.schedules.prices.hint')}</p>
          {prices.length === 0 ? (
            <div className="state state-empty state-compact">{t('admin.schedules.prices.empty')}</div>
          ) : (
            <table className="table price-table">
              <thead>
                <tr>
                  <th>{t('admin.schedules.prices.product')}</th>
                  <th className="num">{t('admin.schedules.prices.base')}</th>
                  <th>{t('admin.schedules.prices.price')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {parsed.map(({ row, prod, currency, amount }) => (
                  <tr key={row.key}>
                    <td>
                      <ProductSelect
                        value={row.productId}
                        products={data.products}
                        categories={data.categories}
                        used={used}
                        label={t('admin.schedules.prices.product')}
                        missingLabel={prod && !prod.archived ? undefined : t('admin.schedules.prices.archived', { name: prod?.name ?? '?' })}
                        onChange={(id) => setPrices((ps) => ps.map((p) => (p.key === row.key ? { ...p, productId: id } : p)))}
                      />
                    </td>
                    <td className="num muted nowrap">{prod ? formatMoney(prod.price.amount, prod.price.currency, lang) : '—'}</td>
                    <td>
                      <div className="price-input">
                        <input
                          className={`input ${row.price.trim() && amount === null ? 'invalid' : ''}`}
                          inputMode="decimal"
                          aria-label={t('admin.schedules.prices.price')}
                          aria-invalid={amount === null}
                          value={row.price}
                          onChange={(e) => setPrices((ps) => ps.map((p) => (p.key === row.key ? { ...p, price: e.target.value } : p)))}
                        />
                        <span className="faint">{currency}</span>
                      </div>
                    </td>
                    <td className="row-actions">
                      <IconButton label={t('admin.schedules.prices.remove')} onClick={() => setPrices((ps) => ps.filter((p) => p.key !== row.key))}>
                        <Trash2 size={16} />
                      </IconButton>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <div className="form rule-ticker">
          <h3>{t('admin.schedules.ticker.title')}</h3>
          <p className="faint">{t('admin.schedules.ticker.hint')}</p>
          <div>
            <Segmented
              value={tickerMode}
              label={t('admin.schedules.ticker.title')}
              onChange={setTickerMode}
              options={(['keep', 'show', 'hide'] as TickerMode[]).map((m) => ({ value: m, label: t(`admin.schedules.ticker.mode.${m}`) }))}
            />
          </div>
          {tickerMode === 'hide' && <p className="muted">{t('admin.schedules.ticker.hideHint')}</p>}
          {tickerOn && (
            <>
              <Field label={t('admin.schedules.ticker.text')}>
                <textarea
                  className="input"
                  rows={2}
                  maxLength={2000}
                  value={tickerText}
                  placeholder={t('admin.schedules.ticker.placeholder')}
                  onChange={(e) => setTickerText(e.target.value)}
                />
              </Field>
              <Select
                label={t('admin.schedules.ticker.template')}
                value={tickerTemplateId}
                hint={scope === 'global' ? t('admin.schedules.ticker.templateHintGlobal') : t('admin.schedules.ticker.templateHint')}
                onChange={(e) => setTickerTemplateId(e.target.value)}
                options={[
                  { value: '', label: t('admin.schedules.ticker.zoneTemplate') },
                  ...data.tickerTemplates.map((x) => ({ value: x.id, label: x.name })),
                ]}
              />
              {previewTemplate && (
                <TickerPreview
                  template={previewTemplate}
                  text={tickerText.trim() || t('admin.schedules.ticker.placeholder')}
                  animate
                  label={t('admin.schedules.ticker.preview')}
                />
              )}
            </>
          )}
        </div>
        <div className="rule-form-foot">
          {problems.length > 0 && (
            <ul className="rule-problems">
              {problems.map((p) => (
                <li key={p}>{p}</li>
              ))}
            </ul>
          )}
          <ErrorText error={save.error} />
        </div>
      </form>
    </Dialog>
  );
}
