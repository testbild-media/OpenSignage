// Time schedules grouped by zone plus global rules: weekly timeline, "what runs now",
// rule editor, enable/disable and delete.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useLocation } from 'react-router';
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { NO_GROUP_ID, type Rule } from '@opensignage/contracts';
import { getLanguage } from '@opensignage/i18n';
import { del, errorText, patch } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useSession } from '../../lib/session';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, IconButton, PageHeader } from '../../components/ui';
import { useRights } from '../../lib/rights';
import { Switch } from '../../components/controls';
import type { Category, Product } from '../products/types';
import { playablePlaylists, type PlaylistSummary, type Zone, type TickerTemplateList } from '../zones/types';
import { NowPanel } from './NowPanel';
import { RuleDialog, type RuleDialogData } from './RuleDialog';
import { Timeline, useNowIn } from './WeekTimeline';
import { formatDate, formatDays, hasPeriod, midnightInfo, nextOccurrence, periodState, weekdayLabels } from './windows';

type Editing = { rule: Rule | null; preset?: { scope: Rule['scope']; zoneId?: string } } | null;

/** The period of a dated rule, its state and – for a zone rule – a link to the zone preview at
 *  its next occurrence (to check e.g. a happy hour weeks ahead). */
function PeriodLine({ rule, today, lang }: { rule: Rule; today: string; lang: string }) {
  const t = useT();
  const w = rule.window!;
  const label =
    w.from && w.until
      ? w.from === w.until
        ? formatDate(w.from, lang)
        : `${formatDate(w.from, lang)} – ${formatDate(w.until, lang)}`
      : w.from
        ? t('admin.schedules.periodFrom', { date: formatDate(w.from, lang) })
        : t('admin.schedules.periodUntil', { date: formatDate(w.until!, lang) });
  const state = periodState(w, today);
  const next = state === 'expired' ? null : nextOccurrence(w, today);
  return (
    <div className="rule-period">
      <span>{label}</span>
      {state === 'expired' && <Badge tone="warning">{t('admin.schedules.expired')}</Badge>}
      {state === 'upcoming' && <Badge tone="primary">{t('admin.schedules.upcoming')}</Badge>}
      {next && rule.scope === 'zone' && rule.zoneId && (
        <Link to={`/zones/${encodeURIComponent(rule.zoneId)}/preview?at=${next}T${w.start}`}>{t('admin.schedules.previewOn', { date: formatDate(next, lang, false) })}</Link>
      )}
    </div>
  );
}

function RulesTable({
  rules,
  playlists,
  zones,
  onEdit,
  onToggle,
  onDelete,
  busyId,
  deleting,
  canEdit,
  today,
}: {
  rules: Rule[];
  playlists: PlaylistSummary[] | undefined;
  zones: Zone[];
  onEdit: (r: Rule) => void;
  onToggle: (r: Rule) => void;
  onDelete: (r: Rule) => void;
  busyId: string | null;
  deleting: boolean;
  /** Rules the user may change (operators limited to zones: rules of their zones). */
  canEdit: (r: Rule) => boolean;
  /** Today in the server time zone (YYYY-MM-DD). */
  today: string;
}) {
  const t = useT();
  const lang = getLanguage();
  const labels = useMemo(() => weekdayLabels(lang), [lang]);
  const playable = new Set(playablePlaylists(playlists).map((p) => p.id));
  const groupName = (r: Rule, id: string): string =>
    id === NO_GROUP_ID ? t('admin.zones.groups.ungrouped') : (zones.find((z) => z.id === r.zoneId)?.groups?.find((g) => g.id === id)?.name ?? '…');
  const sorted = [...rules].sort((a, b) => (a.scope === 'default' ? -1 : 0) - (b.scope === 'default' ? -1 : 0));
  return (
    <table className="table rules-table">
      <thead>
        <tr>
          <th>{t('admin.schedules.field.name')}</th>
          <th>{t('admin.schedules.when')}</th>
          <th>{t('admin.schedules.field.playlist')}</th>
          <th className="num">{t('admin.schedules.prices.title')}</th>
          <th>{t('admin.schedules.field.enabled')}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        {sorted.map((r) => {
          const info = r.window ? midnightInfo(r.window.start, r.window.end) : null;
          const pl = r.playlistId ? playlists?.find((p) => p.id === r.playlistId) : undefined;
          return (
            <tr key={r.id} className={r.enabled ? '' : 'rule-disabled'}>
              <td>
                {canEdit(r) ? (
                  <button type="button" className="link-btn strong" onClick={() => onEdit(r)}>
                    {r.name}
                  </button>
                ) : (
                  <strong>{r.name}</strong>
                )}
                {r.scope === 'default' && (
                  <div>
                    <Badge tone="primary">{t('admin.schedules.scope.default')}</Badge>
                  </div>
                )}
              </td>
              <td className="nowrap">
                {r.window ? (
                  <>
                    <div>{formatDays(r.window.days, (d) => labels[d] ?? '')}</div>
                    <div className="muted">
                      {r.window.start}–{r.window.end} {info === 'crosses' && <Badge>{t('admin.schedules.nextDay')}</Badge>}
                      {info === 'full_day' && <Badge>{t('admin.schedules.fullDay')}</Badge>}
                    </div>
                    {hasPeriod(r.window) && <PeriodLine rule={r} today={today} lang={lang} />}
                  </>
                ) : (
                  <span className="muted">{t('admin.schedules.always')}</span>
                )}
              </td>
              <td>
                {r.ticker && (
                  <div>
                    <Badge tone="primary">{r.ticker.hide ? t('admin.schedules.ticker.hiddenBadge') : t('admin.schedules.ticker.badge')}</Badge>
                  </div>
                )}
                {(r.groupPlaylists ?? []).map((g) => (
                  <div key={g.groupId} className="muted">
                    {groupName(r, g.groupId)}: {playlists?.find((p) => p.id === g.playlistId)?.name ?? '…'}
                  </div>
                ))}
                {!r.playlistId ? (
                  (r.groupPlaylists ?? []).length === 0 && <Badge>{t('admin.schedules.pricesOnly')}</Badge>
                ) : (
                  <>
                    <span>{pl?.name ?? '…'}</span>
                    {playlists && !playable.has(r.playlistId) && (
                      <div>
                        <Badge tone="warning">{t('admin.schedules.playlistUnavailable')}</Badge>
                      </div>
                    )}
                  </>
                )}
              </td>
              <td className="num">{r.prices.length || '—'}</td>
              <td>
                {canEdit(r) ? (
                  <Switch checked={r.enabled} busy={busyId === r.id} label={r.enabled ? t('admin.schedules.on') : t('admin.schedules.off')} onChange={() => onToggle(r)} />
                ) : (
                  <span className="muted">{r.enabled ? t('admin.schedules.on') : t('admin.schedules.off')}</span>
                )}
              </td>
              <td className="row-actions">
                {canEdit(r) && (
                  <>
                    <IconButton label={t('common.action.edit')} onClick={() => onEdit(r)}>
                      <Pencil size={16} />
                    </IconButton>
                    <IconButton label={t('common.action.delete')} disabled={deleting} onClick={() => onDelete(r)}>
                      <Trash2 size={16} />
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

export function SchedulesPage() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const location = useLocation();
  const { server } = useSession();
  const tz = server?.timezone || 'UTC';
  const today = useNowIn(tz).date;
  const rulesQ = useQuery<{ schedules: Rule[] }>('/api/v1/schedules');
  const zonesQ = useQuery<{ zones: Zone[] }>('/api/v1/zones');
  const playlistsQ = useQuery<{ playlists: PlaylistSummary[] }>('/api/v1/playlists');
  const productsQ = useQuery<{ products: Product[] }>('/api/v1/products');
  const archivedQ = useQuery<{ products: Product[] }>('/api/v1/products?archived=1');
  const catsQ = useQuery<{ categories: Category[] }>('/api/v1/categories');
  const tickersQ = useQuery<TickerTemplateList>('/api/v1/ticker-templates');
  useEvents(['zones'], () => {
    rulesQ.reload();
    zonesQ.reload();
  });
  useEvents(['content'], () => playlistsQ.reload());
  useEvents(['products'], () => {
    productsQ.reload();
    archivedQ.reload();
    catsQ.reload();
  });
  const [editing, setEditing] = useState<Editing>(null);
  const [toggling, setToggling] = useState<string | null>(null);
  const close = useCallback(() => setEditing(null), []);

  const toggle = useAction(async (r: Rule) => {
    setToggling(r.id);
    try {
      await patch(`/api/v1/schedules/${r.id}`, { revision: r.revision, enabled: !r.enabled });
      toast(r.enabled ? t('admin.schedules.disabledDone', { name: r.name }) : t('admin.schedules.enabledDone', { name: r.name }));
    } catch (e) {
      toast(errorText(e), 'error');
    } finally {
      setToggling(null);
      rulesQ.reload();
    }
  });
  const remove = useAction(async (r: Rule) => {
    const ok = await confirm({ title: t('admin.schedules.delete'), message: t('admin.schedules.deleteConfirm', { name: r.name }), confirmLabel: t('common.action.delete'), danger: true });
    if (!ok) return;
    try {
      await del(`/api/v1/schedules/${r.id}`);
      toast(t('admin.schedules.deleted', { name: r.name }));
    } catch (e) {
      toast(errorText(e), 'error');
    }
    rulesQ.reload();
  });

  const rights = useRights();
  const rules = useMemo(() => rulesQ.data?.schedules ?? [], [rulesQ.data]);
  const zones = useMemo(() => zonesQ.data?.zones ?? [], [zonesQ.data]);
  const globals = rules.filter((r) => r.scope === 'global');
  // Operators limited to zones change the zone and default rules of zones they may edit.
  const canEdit = (r: Rule): boolean => rights.unrestricted || (r.scope !== 'global' && rights.canEditZone(r.zoneId));
  const editZones = zones.filter((z) => rights.canEditZone(z.id));
  const open = (r: Rule): void => {
    if (canEdit(r)) setEditing({ rule: r });
  };
  const dialogData: RuleDialogData = {
    zones: rights.unrestricted ? zones : editZones,
    globalAllowed: rights.canManageGlobal,
    playlists: playlistsQ.data?.playlists,
    products: productsQ.data?.products ?? [],
    archivedProducts: archivedQ.data?.products ?? [],
    categories: catsQ.data?.categories ?? [],
    tickerTemplates: tickersQ.data?.tickerTemplates ?? [],
    defaultTicker: tickersQ.data?.default,
  };
  const dialogReady = !!zonesQ.data && !!productsQ.data && !!catsQ.data;

  // Deep link from the zone list: /schedules#zone-<id>.
  useEffect(() => {
    if (!location.hash || !zonesQ.data) return;
    document.getElementById(decodeURIComponent(location.hash.slice(1)))?.scrollIntoView({ block: 'start' });
  }, [location.hash, zonesQ.data]);

  const tableProps = {
    today,
    playlists: playlistsQ.data?.playlists,
    zones,
    onEdit: open,
    canEdit,
    onToggle: (r: Rule) => void toggle.run(r),
    onDelete: (r: Rule) => void remove.run(r),
    busyId: toggling,
    deleting: remove.busy,
  };

  return (
    <div className="page">
      <PageHeader
        title={t('admin.schedules.title')}
        subtitle={t('admin.schedules.subtitle')}
        actions={
          (rights.canManageGlobal || editZones.length > 0) && (
            <Button
              variant="primary"
              icon={<Plus size={16} />}
              disabled={!dialogReady}
              onClick={() => setEditing({ rule: null, preset: { scope: editZones.length ? 'zone' : 'global', zoneId: editZones[0]?.id } })}
            >
              {t('admin.schedules.add')}
            </Button>
          )
        }
      />
      <details className="card priority-card">
        <summary>{t('admin.schedules.priority.title')}</summary>
        <ol className="steps">
          <li>{t('admin.schedules.priority.closed')}</li>
          <li>{t('admin.schedules.priority.zone')}</li>
          <li>{t('admin.schedules.priority.global')}</li>
          <li>{t('admin.schedules.priority.default')}</li>
          <li>{t('admin.schedules.priority.none')}</li>
        </ol>
        <p className="muted">{t('admin.schedules.priority.perItem')}</p>
        <p className="muted">{t('admin.schedules.priority.prices')}</p>
      </details>
      <DataState loading={(rulesQ.loading && !rulesQ.data) || (zonesQ.loading && !zonesQ.data)} error={rulesQ.error ?? zonesQ.error} onRetry={() => (rulesQ.reload(), zonesQ.reload())}>
        <section className="card schedule-section" id="global">
          <div className="schedule-section-head">
            <div>
              <h2>{t('admin.schedules.globalTitle')}</h2>
              <div className="muted">{t('admin.schedules.globalHint')}</div>
            </div>
            {rights.canManageGlobal && (
              <Button icon={<Plus size={16} />} disabled={!dialogReady} onClick={() => setEditing({ rule: null, preset: { scope: 'global' } })}>
                {t('admin.schedules.addGlobal')}
              </Button>
            )}
          </div>
          <Timeline rules={globals} timeZone={tz} showZoneLane={false} onOpen={open} />
          {globals.length === 0 ? <div className="state state-empty state-compact">{t('admin.schedules.emptyGlobal')}</div> : <RulesTable rules={globals} {...tableProps} />}
        </section>
        {zones.length === 0 && (
          <div className="state state-empty">
            <div>{t('admin.schedules.noZones')}</div>
            <Link to="/zones" className="btn btn-secondary">
              {t('admin.nav.zones')}
            </Link>
          </div>
        )}
        {zones.map((z) => {
          const own = rules.filter((r) => r.zoneId === z.id);
          const hasDefault = own.some((r) => r.scope === 'default');
          return (
            <section key={z.id} className="card schedule-section" id={`zone-${z.id}`}>
              <div className="schedule-section-head">
                <div>
                  <h2>
                    {z.name} {z.state === 'CLOSED' && <Badge tone="warning">{t('admin.zones.closed')}</Badge>}
                  </h2>
                  {!hasDefault && <div className="muted">{t('admin.schedules.noDefault')}</div>}
                </div>
                {rights.canEditZone(z.id) ? (
                  <div className="page-actions">
                    {!hasDefault && (
                      <Button disabled={!dialogReady} onClick={() => setEditing({ rule: null, preset: { scope: 'default', zoneId: z.id } })}>
                        {t('admin.schedules.addDefault')}
                      </Button>
                    )}
                    <Button icon={<Plus size={16} />} disabled={!dialogReady} onClick={() => setEditing({ rule: null, preset: { scope: 'zone', zoneId: z.id } })}>
                      {t('admin.schedules.addZone')}
                    </Button>
                  </div>
                ) : (
                  <Badge>{t('admin.readOnly.badge')}</Badge>
                )}
              </div>
              <NowPanel zoneId={z.id} rules={rules} />
              <Timeline rules={[...own, ...globals]} timeZone={tz} showZoneLane onOpen={open} />
              {own.length === 0 ? <div className="state state-empty state-compact">{t('admin.schedules.emptyZone')}</div> : <RulesTable rules={own} {...tableProps} />}
            </section>
          );
        })}
      </DataState>
      {editing && dialogReady && (
        <RuleDialog
          rule={editing.rule}
          preset={editing.preset}
          data={dialogData}
          onClose={close}
          onSaved={() => {
            setEditing(null);
            rulesQ.reload();
          }}
        />
      )}
    </div>
  );
}
