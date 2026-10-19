// "What runs now" for one zone: the server evaluates the same rules as the clients
// (GET /api/v1/schedules/now?zone=).
import { useEffect } from 'react';
import { Link } from 'react-router';
import { CircleOff, Eye, MoonStar, CirclePlay } from 'lucide-react';
import { NO_GROUP_ID, type Money, type Rule } from '@opensignage/contracts';
import { useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { Badge, DataState } from '../../components/ui';

interface NowResult {
  zoneId: string;
  at: string;
  timezone: string;
  zoneState: 'OPEN' | 'CLOSED';
  kind: 'closed' | 'rule' | 'cleared';
  playlist: { id: string; name: string } | null;
  ruleId?: string;
  activeRules: { id: string; name: string; scope: Rule['scope']; hasPlaylist: boolean; hasTicker: boolean }[];
  prices: Record<string, Money>;
  ticker: { text: string; ruleId: string } | null;
  /** Rule that hides the ticker now. */
  tickerHiddenBy?: string;
  /** What every display group plays (empty without groups). */
  groups?: { groupId: string; name?: string; kind: NowResult['kind']; playlist: { id: string; name: string } | null; ruleId?: string; devices: number }[];
}

export function NowPanel({ zoneId, rules }: { zoneId: string; rules: Rule[] }) {
  const t = useT();
  const q = useQuery<NowResult>(`/api/v1/schedules/now?zone=${encodeURIComponent(zoneId)}`);
  useEvents(['zones', 'content', 'live'], (e) => {
    if (e.topic === '*' || !e.zoneId || e.zoneId === zoneId) q.reload();
  });
  useEffect(() => {
    // Windows start and end on the minute: re-evaluate regularly.
    const id = window.setInterval(q.reload, 30000);
    return () => clearInterval(id);
  }, [q.reload]);
  const d = q.data;
  const rule = d?.ruleId ? rules.find((r) => r.id === d.ruleId) : undefined;
  const priceCount = d ? Object.keys(d.prices).length : 0;
  const at = d ? d.at.slice(11, 16) : '';
  return (
    <div className={`now-panel now-${d?.kind ?? 'loading'}`} aria-live="polite">
      <div className="now-title">
        {t('admin.schedules.now.title')}
        <Link to={`/zones/${zoneId}/preview`} className="now-preview">
          <Eye size={14} />
          {t('admin.zones.preview.action')}
        </Link>
      </div>
      <DataState loading={q.loading && !d} error={q.error} onRetry={q.reload}>
        {d && (
          <div className="now-body">
            {(d.groups ?? []).length > 0 && (
              <ul className="now-groups">
                {d.groups!.map((g) => (
                  <li key={g.groupId}>
                    <span className="strong">{g.groupId === NO_GROUP_ID ? t('admin.zones.groups.ungrouped') : g.name}</span>
                    <span className="faint"> ({g.devices})</span>: {g.playlist?.name ?? '—'}
                    {g.kind === 'closed' && <Badge tone="warning">{t('admin.zones.closed')}</Badge>}
                  </li>
                ))}
              </ul>
            )}
            <div className="now-main">
              {d.kind === 'closed' && <MoonStar size={18} />}
              {d.kind === 'rule' && <CirclePlay size={18} />}
              {d.kind === 'cleared' && <CircleOff size={18} />}
              <span>
                {d.kind === 'closed' && t('admin.schedules.now.closed', { playlist: d.playlist?.name ?? '' })}
                {d.kind === 'rule' && t('admin.schedules.now.rule', { playlist: d.playlist?.name ?? '', rule: rule?.name ?? d.activeRules.find((r) => r.id === d.ruleId)?.name ?? '' })}
                {d.kind === 'cleared' && t('admin.schedules.now.cleared')}
              </span>
            </div>
            <div className="now-meta">
              {d.zoneState === 'CLOSED' && <Badge tone="warning">{t('admin.zones.closed')}</Badge>}
              {d.activeRules.map((r) => (
                <Badge key={r.id} tone={r.id === d.ruleId ? 'primary' : 'neutral'}>
                  {r.name}
                </Badge>
              ))}
              {priceCount > 0 && <span className="muted">{t('admin.schedules.now.prices', { count: priceCount })}</span>}
              {d.ticker && (
                <span className="muted now-ticker" title={d.ticker.text}>
                  {t('admin.schedules.now.ticker', { text: d.ticker.text })}
                </span>
              )}
              {d.tickerHiddenBy && (
                <span className="muted">{t('admin.schedules.now.tickerHidden', { rule: d.activeRules.find((r) => r.id === d.tickerHiddenBy)?.name ?? '' })}</span>
              )}
              <span className="faint">{t('admin.schedules.now.at', { time: at, tz: d.timezone })}</span>
            </div>
          </div>
        )}
      </DataState>
    </div>
  );
}
