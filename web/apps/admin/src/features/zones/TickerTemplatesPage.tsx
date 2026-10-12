// Ticker templates: font (bundled and uploaded fonts, sorted by name, each shown in its own
// font), size, colours, background, speed, direction, height and position, with a live
// preview.
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router';
import { ChevronRight, Pencil, Plus, Trash2 } from 'lucide-react';
import type { TickerTemplate } from '@opensignage/contracts';
import { del, patch, post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { byFamily, loadCustomFonts, loadFontManifest } from '../../lib/fonts';
import { useConfirm, useToast } from '../../components/feedback';
import { Button, DataState, Dialog, ErrorText, Field, IconButton, PageHeader, TextInput } from '../../components/ui';
import { ColorField, FontSelect, Segmented, validColor } from '../../components/controls';
import { TickerPreview } from './TickerPreview';
import type { TickerTemplateList, TickerTemplateRecord, Zone } from './types';

export const TICKER_LIMITS = {
  size: [8, 400],
  speed: [10, 2000],
  height: [16, 540],
} as const;

/** Client-side check mirroring contracts.TickerTemplate.Validate (font checked by server). */
export function tickerProblems(tpl: TickerTemplate): string[] {
  const out: string[] = [];
  const within = (v: number, [lo, hi]: readonly [number, number]): boolean => Number.isFinite(v) && v >= lo && v <= hi;
  if (!tpl.font) out.push('font');
  if (!within(tpl.size, TICKER_LIMITS.size)) out.push('size');
  if (!within(tpl.speed, TICKER_LIMITS.speed)) out.push('speed');
  if (!within(tpl.height, TICKER_LIMITS.height)) out.push('height');
  if (!validColor(tpl.color)) out.push('color');
  if (!validColor(tpl.background)) out.push('background');
  return out;
}

function useFontFamilies(): string[] {
  const [families, setFamilies] = useState<string[]>([]);
  useEffect(() => {
    let alive = true;
    void Promise.all([loadFontManifest(), loadCustomFonts(true)]).then(([m, custom]) => {
      const names = new Set([...(m?.families ?? []).map((f) => f.family), ...custom]);
      if (alive && names.size > 0) setFamilies([...names].sort(byFamily));
    });
    return () => {
      alive = false;
    };
  }, []);
  return families;
}

function NumberField({ label, value, onChange, range, unit }: { label: string; value: number; onChange: (v: number) => void; range: readonly [number, number]; unit: string }) {
  const t = useT();
  const bad = !(value >= range[0] && value <= range[1]);
  return (
    <TextInput
      label={`${label} (${unit})`}
      type="number"
      min={range[0]}
      max={range[1]}
      value={Number.isFinite(value) ? value : ''}
      onChange={(e) => onChange(e.target.value === '' ? NaN : Number(e.target.value))}
      error={bad ? t('admin.zones.tickers.range', { min: range[0], max: range[1] }) : null}
    />
  );
}

function TemplateDialog({
  record,
  defaults,
  families,
  onClose,
  onSaved,
}: {
  record: TickerTemplateRecord | null;
  defaults: TickerTemplate;
  families: string[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const [name, setName] = useState(record?.name ?? '');
  const [tpl, setTpl] = useState<TickerTemplate>(record?.template ?? defaults);
  const [sample, setSample] = useState(t('admin.zones.tickers.sampleText'));
  const set = <K extends keyof TickerTemplate>(k: K, v: TickerTemplate[K]): void => setTpl((x) => ({ ...x, [k]: v }));
  const problems = tickerProblems(tpl);
  const valid = name.trim() !== '' && problems.length === 0;
  const save = useAction(async () => {
    const body = { name: name.trim(), template: tpl };
    if (record) await patch(`/api/v1/ticker-templates/${record.id}`, { ...body, revision: record.revision });
    else await post('/api/v1/ticker-templates', body);
    toast(t('admin.saved'));
    onSaved();
  });
  return (
    <Dialog
      title={record ? t('admin.zones.tickers.edit') : t('admin.zones.tickers.add')}
      onClose={onClose}
      wide
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" type="submit" form="ticker-form" busy={save.busy} disabled={!valid}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <form
        id="ticker-form"
        className="ticker-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) void save.run();
        }}
      >
        <div className="form">
          <TextInput label={t('admin.zones.tickers.name')} value={name} maxLength={200} onChange={(e) => setName(e.target.value)} />
          <FontSelect label={t('admin.zones.tickers.font')} value={tpl.font} families={families} onChange={(f) => set('font', f)} hint={families.length === 0 ? t('admin.zones.tickers.fontsLoading') : undefined} />
          <div className="form-row">
            <NumberField label={t('admin.zones.tickers.size')} unit="px" value={tpl.size} range={TICKER_LIMITS.size} onChange={(v) => set('size', v)} />
            <NumberField label={t('admin.zones.tickers.height')} unit="px" value={tpl.height} range={TICKER_LIMITS.height} onChange={(v) => set('height', v)} />
          </div>
          <div className="form-row">
            <ColorField label={t('admin.zones.tickers.color')} value={tpl.color} alpha onChange={(v) => set('color', v)} opacityLabel={t('admin.zones.tickers.opacity')} invalidText={t('admin.zones.tickers.colorInvalid')} />
            <ColorField label={t('admin.zones.tickers.background')} value={tpl.background} alpha onChange={(v) => set('background', v)} opacityLabel={t('admin.zones.tickers.opacity')} invalidText={t('admin.zones.tickers.colorInvalid')} />
          </div>
          <NumberField label={t('admin.zones.tickers.speed')} unit={t('admin.zones.tickers.speedUnit')} value={tpl.speed} range={TICKER_LIMITS.speed} onChange={(v) => set('speed', v)} />
          <div className="ticker-choices">
            <Field label={t('admin.zones.tickers.direction')}>
              <div>
                <Segmented
                  value={tpl.direction}
                  label={t('admin.zones.tickers.direction')}
                  onChange={(v) => set('direction', v)}
                  options={[
                    { value: 'left', label: t('admin.zones.tickers.dirLeft') },
                    { value: 'right', label: t('admin.zones.tickers.dirRight') },
                  ]}
                />
              </div>
            </Field>
            <Field label={t('admin.zones.tickers.position')}>
              <div>
                <Segmented
                  value={tpl.position}
                  label={t('admin.zones.tickers.position')}
                  onChange={(v) => set('position', v)}
                  options={[
                    { value: 'top', label: t('admin.zones.tickers.posTop') },
                    { value: 'bottom', label: t('admin.zones.tickers.posBottom') },
                  ]}
                />
              </div>
            </Field>
          </div>
          <ErrorText error={save.error} />
        </div>
        <div className="ticker-form-preview">
          <TickerPreview template={tpl} text={sample || ' '} animate label={t('admin.zones.tickers.preview')} />
          <TextInput label={t('admin.zones.tickers.sample')} value={sample} onChange={(e) => setSample(e.target.value)} hint={t('admin.zones.tickers.sampleHint')} />
        </div>
      </form>
    </Dialog>
  );
}

export function TickerTemplatesPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery<TickerTemplateList>('/api/v1/ticker-templates');
  const zones = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  useEvents(['zones'], () => {
    q.reload();
    zones.reload();
  });
  const families = useFontFamilies();
  const [editing, setEditing] = useState<TickerTemplateRecord | 'new' | null>(null);
  const close = useCallback(() => setEditing(null), []);
  const remove = useAction(async (r: TickerTemplateRecord) => {
    const ok = await confirm({ title: t('admin.zones.tickers.delete'), message: t('admin.zones.tickers.deleteConfirm', { name: r.name }), confirmLabel: t('common.action.delete'), danger: true });
    if (!ok) return;
    await del(`/api/v1/ticker-templates/${r.id}`);
    toast(t('admin.zones.tickers.deleted', { name: r.name }));
    q.reload();
  });
  const list = q.data?.tickerTemplates ?? [];
  const usedBy = (id: string): string[] => (zones.data?.zones ?? []).filter((z) => z.tickerTemplateId === id).map((z) => z.name);
  return (
    <div className="page">
      <div className="crumbs">
        <Link to="/zones">{t('admin.zones.title')}</Link>
        <ChevronRight size={14} />
      </div>
      <PageHeader
        title={t('admin.zones.tickers.title')}
        subtitle={t('admin.zones.tickers.subtitle')}
        actions={
          <Button variant="primary" icon={<Plus size={16} />} disabled={!q.data} onClick={() => setEditing('new')}>
            {t('admin.zones.tickers.add')}
          </Button>
        }
      />
      <ErrorText error={remove.error} />
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload} empty={list.length === 0} emptyText={t('admin.zones.tickers.empty')}>
        <div className="ticker-grid">
          {list.map((r) => {
            const used = usedBy(r.id);
            return (
              <div key={r.id} className="ticker-card">
                <TickerPreview template={r.template} text={t('admin.zones.tickers.sampleText')} animate={false} label={r.name} />
                <div className="ticker-card-body">
                  <div className="ticker-card-text">
                    <div className="strong ellipsis">{r.name}</div>
                    <div className="faint ellipsis">
                      {r.template.font} · {r.template.size}px · {t(r.template.position === 'top' ? 'admin.zones.tickers.posTop' : 'admin.zones.tickers.posBottom')}
                    </div>
                    <div className="faint ellipsis">{used.length ? t('admin.zones.tickers.usedBy', { names: used.join(', ') }) : t('admin.zones.tickers.unused')}</div>
                  </div>
                  <span className="row-actions">
                    <IconButton label={t('common.action.edit')} onClick={() => setEditing(r)}>
                      <Pencil size={16} />
                    </IconButton>
                    <IconButton label={t('common.action.delete')} disabled={remove.busy} onClick={() => void remove.run(r)}>
                      <Trash2 size={16} />
                    </IconButton>
                  </span>
                </div>
              </div>
            );
          })}
        </div>
      </DataState>
      {editing && q.data && (
        <TemplateDialog
          record={editing === 'new' ? null : editing}
          defaults={q.data.default}
          families={families}
          onClose={close}
          onSaved={() => {
            setEditing(null);
            q.reload();
          }}
        />
      )}
    </div>
  );
}
