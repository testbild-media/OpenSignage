// Zone preview: every screen of a zone as its player shows it – now, or at a chosen moment
// (the time runs on from there), with the real or a chosen zone state. Works without any
// device online: the server evaluates the schedule from the deployment like the players.
import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import { ArrowLeft, CalendarClock } from 'lucide-react';
import type { Typography } from '@opensignage/contracts';
import { LiveStore, contentFromManifest } from '@opensignage/render';
import { formatMoney, getLanguage } from '@opensignage/i18n';
import { ApiError, get } from '../../lib/api';
import { useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { loadCustomFonts } from '../../lib/fonts';
import { Badge, DataState, PageHeader } from '../../components/ui';
import { Segmented } from '../../components/controls';
import { ScreenTile } from './ScreenTile';
import { inputValue, manifestSignature, playlistNames, previewQuery, priceChanges, soldOutNames, untilNextMinute, type PreviewScreen, type StateChoice, type ZonePreview } from './preview';

const LIST_MAX = 8;

export function ZonePreviewPage() {
  const t = useT();
  const { id = '' } = useParams();
  const lang = getLanguage();
  // ?at=YYYY-MM-DDTHH:MM opens the preview at that moment (e.g. from a dated schedule rule).
  const [params] = useSearchParams();
  const linked = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(params.get('at') ?? '') ? params.get('at')! : '';
  const [mode, setMode] = useState<'now' | 'at'>(linked ? 'at' : 'now');
  const [atInput, setAtInput] = useState(linked);
  const [stateChoice, setStateChoice] = useState<StateChoice>('');
  const [data, setData] = useState<ZonePreview | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const live = useMemo(() => new LiveStore(), []);
  // The previewed clock as an offset to the real time: 0 = now; null = a moment was chosen
  // and the first answer sets the offset (the server knows the time zone).
  const offset = useRef<number | null>(0);
  const [tick, setTick] = useState(0);
  const chosen = mode === 'at' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(atInput);

  useEffect(() => {
    void loadCustomFonts();
  }, []);

  // A chosen moment starts the previewed clock there; from then on it runs on.
  useEffect(() => {
    offset.current = chosen ? null : 0;
  }, [chosen, atInput]);

  const load = useRef<() => void>(() => {});
  load.current = () => {
    const off = offset.current;
    // Refreshes of a chosen moment ask for the previewed time running on from there.
    const at = !chosen ? null : off === null ? atInput : new Date(Date.now() + off).toISOString();
    get<ZonePreview>(`/api/v1/zones/${encodeURIComponent(id)}/preview${previewQuery(at, stateChoice)}`).then(
      (r) => {
        if (chosen && offset.current === null) offset.current = Date.parse(r.at) - Date.now();
        live.setLanguage(lang);
        live.setClock(chosen ? (offset.current ?? 0) : 0, r.timezone);
        live.set(r.live, r.prices, r.ticker);
        setData(r);
        setError(null);
        setLoading(false);
      },
      (e: unknown) => {
        setError(e);
        setLoading(false);
      },
    );
  };
  useEffect(() => {
    load.current();
  }, [id, chosen, atInput, stateChoice]);
  useEvents(['content', 'live', 'products', 'zones', 'devices'], (e) => {
    if (e.topic === '*' || !e.zoneId || e.zoneId === id) load.current();
  });
  // Windows of time schedules start on whole minutes: evaluate again after every minute of
  // the previewed clock; the shown time ticks every second.
  useEffect(() => {
    let timer = 0;
    const next = (): void => {
      timer = window.setTimeout(() => {
        load.current();
        next();
      }, untilNextMinute(live.now().getTime()));
    };
    next();
    const clock = window.setInterval(() => setTick((x) => x + 1), 1000);
    return () => {
      clearTimeout(timer);
      clearInterval(clock);
    };
  }, [live, chosen, atInput]);

  const signature = data ? manifestSignature(data.manifest) : '';
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const content = useMemo(() => (data ? contentFromManifest(data.manifest) : null), [signature]);
  const names = useMemo(() => (data ? playlistNames(data.manifest) : new Map<string, string>()), [data]);
  const typography = data?.manifest.presentations[0]?.typography as Typography | undefined;
  const notFound = error instanceof ApiError && error.status === 404;

  const shownTime = data
    ? new Intl.DateTimeFormat(lang, { timeZone: data.timezone, weekday: 'short', day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(live.now())
    : '';
  void tick;

  const ruleName = (ruleId?: string): string => data?.manifest.rules.find((r) => r.id === ruleId)?.name ?? '';
  const place = (s: PreviewScreen): string => `${s.groupName || t('admin.zones.groups.ungrouped')} · ${t('devices.placeN', { place: s.position })}`;
  const screenTitle = (s: PreviewScreen): string => s.name || place(s);
  const source = (s: PreviewScreen): string => {
    const pl = s.playlistId ? (names.get(s.playlistId) ?? '?') : t('admin.zones.preview.noPlaylist');
    if (s.kind === 'closed') return `${pl} · ${t('admin.zones.preview.kind.closed')}`;
    if (s.kind === 'cleared') return t('admin.schedules.now.cleared');
    return `${pl} · ${ruleName(s.ruleId)}`;
  };

  const changes = data ? priceChanges(data.prices, data.live.products) : [];
  const soldOut = data ? soldOutNames(data.live.products) : [];

  return (
    <div className="page zone-preview">
      <PageHeader
        title={data ? t('admin.zones.preview.title', { name: data.zoneName }) : t('admin.zones.preview.action')}
        subtitle={t('admin.zones.preview.subtitle')}
        actions={
          <>
            <Link to="/zones" className="btn btn-secondary">
              <ArrowLeft size={16} />
              {t('admin.nav.zones')}
            </Link>
            <Link to={`/schedules#zone-${id}`} className="btn btn-secondary">
              <CalendarClock size={16} />
              {t('admin.zones.schedules')}
            </Link>
          </>
        }
      />
      <div className="preview-toolbar">
        <Segmented
          value={mode}
          label={t('admin.zones.preview.when')}
          onChange={(v) => {
            if (v === 'at' && !atInput && data) setAtInput(inputValue(live.now(), data.timezone));
            setMode(v);
          }}
          options={[
            { value: 'now', label: t('admin.zones.preview.now') },
            { value: 'at', label: t('admin.zones.preview.at') },
          ]}
        />
        {mode === 'at' && (
          <label className="preview-at">
            <span className="faint">{t('admin.zones.preview.atLabel', { timezone: data?.timezone ?? '' })}</span>
            <input type="datetime-local" className="input" value={atInput} onChange={(e) => setAtInput(e.target.value)} />
          </label>
        )}
        <Segmented
          value={stateChoice}
          label={t('admin.zones.preview.state')}
          onChange={setStateChoice}
          options={[
            { value: '', label: t('admin.zones.preview.stateAsIs') },
            { value: 'OPEN', label: t('admin.zones.open') },
            { value: 'CLOSED', label: t('admin.zones.closed') },
          ]}
        />
        {data && <span className="preview-clock">{t('admin.zones.preview.showing', { time: shownTime })}</span>}
      </div>
      <DataState loading={loading && !data} error={notFound ? null : error} onRetry={() => load.current()} empty={notFound} emptyText={t('admin.zones.preview.notFound')}>
        {data && content && (
          <>
            <div className="preview-summary">
              <section>
                <h3>{t('admin.zones.preview.rules')}</h3>
                {data.activeRules.length ? (
                  <div className="preview-chips">
                    {data.activeRules.map((r) => (
                      <Badge key={r.id}>{r.name}</Badge>
                    ))}
                  </div>
                ) : (
                  <p className="faint">{t('admin.zones.preview.noRules')}</p>
                )}
                <p className="preview-state">
                  {data.zoneState === 'OPEN' ? <Badge tone="success">{t('admin.zones.open')}</Badge> : <Badge tone="warning">{t('admin.zones.closed')}</Badge>}
                </p>
              </section>
              <section>
                <h3>{t('admin.zones.preview.ticker')}</h3>
                <p className={data.ticker ? '' : 'faint'}>{data.ticker?.text || t('admin.zones.preview.noTicker')}</p>
              </section>
              <section>
                <h3>{t('admin.zones.preview.prices')}</h3>
                {changes.length ? (
                  <ul className="preview-list">
                    {changes.slice(0, LIST_MAX).map((c) => (
                      <li key={c.id}>
                        <span className="ellipsis">{c.name}</span>
                        <span className="nowrap">
                          <s className="faint">{formatMoney(c.base.amount, c.base.currency, lang)}</s> {formatMoney(c.price.amount, c.price.currency, lang)}
                        </span>
                      </li>
                    ))}
                    {changes.length > LIST_MAX && <li className="faint">{t('admin.zones.preview.more', { count: changes.length - LIST_MAX })}</li>}
                  </ul>
                ) : (
                  <p className="faint">{t('admin.zones.preview.noPrices')}</p>
                )}
              </section>
              <section>
                <h3>{t('admin.zones.preview.soldOut')}</h3>
                {soldOut.length ? (
                  <ul className="preview-list">
                    {soldOut.slice(0, LIST_MAX).map((n) => (
                      <li key={n}>
                        <span className="ellipsis">{n}</span>
                      </li>
                    ))}
                    {soldOut.length > LIST_MAX && <li className="faint">{t('admin.zones.preview.more', { count: soldOut.length - LIST_MAX })}</li>}
                  </ul>
                ) : (
                  <p className="faint">{t('admin.zones.preview.noneSoldOut')}</p>
                )}
              </section>
            </div>
            <div className="preview-grid">
              {data.screens.map((s) => {
                const key = s.deviceId ?? `${s.groupId ?? ''}#${s.position}`;
                return (
                  <figure key={key} className={`preview-tile ${s.width < s.height ? 'portrait' : ''}`}>
                    <figcaption>
                      <span className="strong ellipsis">{screenTitle(s)}</span>
                      {s.deviceId ? (
                        <Badge tone={s.online ? 'success' : 'neutral'}>{s.online ? t('devices.status.online') : t('devices.status.offline')}</Badge>
                      ) : (
                        <Badge>{t('admin.zones.preview.noDevice')}</Badge>
                      )}
                    </figcaption>
                    <ScreenTile
                      content={content}
                      live={live}
                      typography={typography}
                      playlistId={s.playlistId ?? null}
                      anchor={s.since ? Date.parse(s.since) : null}
                      position={s.position}
                      scaleMode={s.scaleMode}
                      width={s.width}
                      height={s.height}
                      label={screenTitle(s)}
                    />
                    <div className="preview-source faint">
                      {source(s)}
                      {s.name ? ` · ${place(s)}` : ''}
                    </div>
                  </figure>
                );
              })}
            </div>
            <p className="faint preview-hint">{t('admin.zones.preview.hint')}</p>
          </>
        )}
      </DataState>
    </div>
  );
}
