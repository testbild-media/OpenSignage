// Device monitoring: state, player, content, sync, version, display,
// load and storage of one device with translated labels and formatted values. Values an
// older agent does not report show "—".
import { useCallback, useState } from 'react';
import { AlertTriangle, CheckCircle2, Eraser, OctagonAlert, ArrowUpCircle } from 'lucide-react';
import type { DeviceStatus } from '@opensignage/contracts';
import { has } from '@opensignage/i18n';
import { post } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { roleAtLeast, useSession } from '../../lib/session';
import { useToast } from '../../components/feedback';
import { Badge, Button, ErrorText } from '../../components/ui';
import { formatBytes, formatDate, formatPercent } from '../media/model';
import { CleanCacheDialog } from './Actions';
import { ProblemBadges } from './Alerts';
import {
  formatPercentValue,
  formatTemperature,
  formatUptime,
  modeLabel,
  signalPercent,
  signalQuality,
  storageOf,
  type Device,
  type DiskLevel,
  unsupportedBoard,
} from './model';

export function StatusBadge({ d }: { d: Device }) {
  const t = useT();
  if (!d.paired) return <Badge>{t('devices.status.unpaired')}</Badge>;
  if (d.online && d.metrics?.mode === 'regeneration')
    return <Badge tone="warning">{t('devices.status.regeneration')}</Badge>;
  if (d.config.blackout) return <Badge tone="warning">{t('devices.status.blackout')}</Badge>;
  return d.online ? (
    <Badge tone="success">{t('devices.status.online')}</Badge>
  ) : (
    <Badge tone="danger">{t('devices.status.offline')}</Badge>
  );
}

export function SyncCell({ d }: { d: Device }) {
  const t = useT();
  const s = d.sync;
  if (!s || !s.state) return <span className="faint">—</span>;
  if (s.state === 'preparing' && s.total > 0)
    return (
      <Badge tone="primary">{t('devices.syncing', { pct: Math.round((s.done / s.total) * 100) })}</Badge>
    );
  if (s.state === 'failed') return <Badge tone="danger">{t('devices.syncFailed')}</Badge>;
  return <Badge>{has(`devices.syncState.${s.state}`) ? t(`devices.syncState.${s.state}`) : s.state}</Badge>;
}

const dash = <span className="faint">—</span>;

/** Technical names of the hardware decoding paths (not translated). */
function videoPathLabel(path: string): string {
  if (path === 'v4l2') return 'V4L2';
  if (path === 'vaapi-vulkan') return 'VA-API';
  return path;
}

function ContentValue({ d }: { d: Device }) {
  const t = useT();
  const kind = d.activeContent?.kind || d.activeSource?.kind;
  if (!kind) return dash;
  const name = d.activeContent?.playlistName;
  const source = has(`devices.source.${kind}`) ? t(`devices.source.${kind}`) : kind;
  if (kind === 'cleared') return <>{source}</>;
  return (
    <>
      <b>{name || '—'}</b> <span className="faint">{source}</span>
    </>
  );
}

function VersionValue({ d }: { d: Device }) {
  const t = useT();
  if (d.versionMatch) return <span className="mono">{d.version || '—'}</span>;
  return (
    <>
      <Badge tone="warning">{t('devices.versionMismatch', { version: d.version || '—' })}</Badge>
      <AlignBlock d={d} />
    </>
  );
}

interface ClientRow {
  id: string;
  online: boolean;
  packageAvailable: boolean;
  update: { state: string; error: string; version: string; updatedAt: string } | null;
}

const IN_FLIGHT = ['waiting', 'sent', 'downloading', 'installing', 'installed'];

/** "Align version" for admins: installs the server version on the device. */
function AlignBlock({ d }: { d: Device }) {
  const t = useT();
  const toast = useToast();
  const { user } = useSession();
  const admin = roleAtLeast(user?.role, 'admin');
  const clients = useQuery<{ serverVersion: string; devices: ClientRow[] }>(
    admin ? '/api/v1/updates/clients' : null,
  );
  useEvents(['updates'], () => clients.reload());
  const align = useAction(async () => {
    await post(`/api/v1/devices/${encodeURIComponent(d.id)}/align`);
    toast(t('devices.align.started'));
    clients.reload();
  });
  if (!admin) return null;
  const c = clients.data?.devices.find((x) => x.id === d.id);
  const u = c?.update;
  const running = !!u && IN_FLIGHT.includes(u.state) && u.version === clients.data?.serverVersion;
  const phase = d.metrics?.update;
  const pct = phase?.total ? Math.round(((phase.done ?? 0) / phase.total) * 100) : null;
  const errText = (raw: string): string => {
    try {
      const e = JSON.parse(raw) as { code?: string };
      if (e.code && has(`errors.${e.code}`)) return t(`errors.${e.code}`);
    } catch {
      // plain text from the agent
    }
    return has(`errors.${raw}`) ? t(`errors.${raw}`) : raw;
  };
  return (
    <div className="align-block">
      <p className="faint">{t('devices.align.hint', { version: clients.data?.serverVersion ?? '…' })}</p>
      {!running && (
        <Button
          icon={<ArrowUpCircle size={16} />}
          busy={align.busy}
          disabled={!d.online || (!!c && !c.packageAvailable)}
          title={
            !d.online
              ? t('updates.alignOffline')
              : c && !c.packageAvailable
                ? t('updates.alignNoPackage')
                : undefined
          }
          onClick={() => void align.run()}
        >
          {t('updates.align')}
        </Button>
      )}
      {c && !c.packageAvailable && <div className="faint">{t('updates.alignNoPackage')}</div>}
      {u && (
        <div className="state-line muted">
          <Badge
            tone={
              u.state === 'done'
                ? 'success'
                : ['failed', 'rolled_back'].includes(u.state)
                  ? 'danger'
                  : running
                    ? 'primary'
                    : 'neutral'
            }
          >
            {has(`updates.duState.${u.state}`) ? t(`updates.duState.${u.state}`) : u.state}
          </Badge>
          <span>{t('updates.toVersion', { version: u.version, time: formatDate(u.updatedAt) })}</span>
        </div>
      )}
      {running && phase?.phase && (
        <div className="faint">
          {has(`client.update.phase.${phase.phase}`) ? t(`client.update.phase.${phase.phase}`) : phase.phase}
          {pct !== null && ` · ${pct} %`}
        </div>
      )}
      {u?.error && <div className="form-error">{errText(u.error)}</div>}
      <ErrorText error={align.error ?? clients.error} />
    </div>
  );
}

/** Wired and wireless interfaces: address, DHCP/static, speed or network and signal. */
function NetworkValue({ network }: { network: DeviceStatus['network'] }) {
  const t = useT();
  if (!network || network.interfaces.length === 0) return <>—</>;
  return (
    <>
      {network.interfaces.map((n) => {
        const kind = n.kind === 'wifi' ? t('client.network.wifiTitle') : t('client.network.ethernetTitle');
        const q = n.signalDbm ? signalQuality(n.signalDbm) : null;
        return (
          <div key={n.name} className="net-line">
            <span className="strong">{kind}</span> <span className="faint">{n.name}</span>
            {n.connected ? (
              <>
                {n.ssid && <> · {n.ssid}</>}
                {n.ipv4?.length ? <> · {n.ipv4.join(', ')}</> : null}
                {n.method && <span className="faint"> · {t(`client.network.method.${n.method}`)}</span>}
                {!!n.speedMbps && <span className="faint"> · {n.speedMbps} Mbit/s</span>}
                {q && n.signalDbm && (
                  <>
                    {' '}
                    <Badge tone={q === 'weak' ? 'danger' : q === 'fair' ? 'warning' : 'success'}>
                      {t(`client.network.quality.${q}`)} · {n.signalDbm} dBm ({signalPercent(n.signalDbm)} %)
                    </Badge>
                  </>
                )}
              </>
            ) : (
              <span className="faint"> · {t('client.network.state.disconnected')}</span>
            )}
          </div>
        );
      })}
      {network.pending && <Badge tone="warning">{t('client.network.pendingBadge')}</Badge>}
    </>
  );
}

/** The monitoring card: everything the device reports, translated and formatted. */
export function MonitorCard({ d }: { d: Device }) {
  const t = useT();
  const m = d.metrics ?? {};
  const disp = m.display;
  const player = m.player;
  const sync = d.sync;
  const syncError = sync?.error
    ? has(`client.syncError.${sync.error}`)
      ? t(`client.syncError.${sync.error}`)
      : sync.error
    : '';
  return (
    <div className="card">
      <h2>{t('devices.mon.title')}</h2>
      <dl className="kv">
        <dt>{t('devices.state')}</dt>
        <dd>
          <StatusBadge d={d} /> <ProblemBadges alerts={d.alerts} showAcked />
        </dd>
        <dt>{t('devices.lastSeen')}</dt>
        <dd>{d.lastSeenAt ? formatDate(d.lastSeenAt) : '—'}</dd>
        <dt>{t('client.status.player')}</dt>
        <dd>
          {player ? (has(`client.player.${player}`) ? t(`client.player.${player}`) : player) : '—'}
          {!!m.playerRestarts && (
            <span className="faint"> · {t('client.status.restarts', { count: m.playerRestarts })}</span>
          )}
        </dd>
        <dt>{t('client.status.mode')}</dt>
        <dd>{m.mode ? (has(`client.mode.${m.mode}`) ? t(`client.mode.${m.mode}`) : m.mode) : '—'}</dd>
        <dt>{t('devices.content')}</dt>
        <dd>
          <ContentValue d={d} />
        </dd>
        <dt>{t('devices.sync')}</dt>
        <dd>
          <SyncCell d={d} />
          {sync?.state === 'preparing' && sync.total > 0 && (
            <span className="faint">
              {' '}
              {formatBytes(sync.done)} / {formatBytes(sync.total)}
            </span>
          )}
          {syncError && <div className="form-error">{syncError}</div>}
        </dd>
        <dt>{t('devices.version')}</dt>
        <dd>
          <VersionValue d={d} />
        </dd>
        <dt>{t('client.status.resolution')}</dt>
        <dd>
          {disp && disp.width > 0 ? (
            <>
              {modeLabel(disp.width, disp.height, disp.refresh)}
              {!!disp.rotation && <span className="faint"> · {disp.rotation}°</span>}
              {disp.output && <span className="faint"> · {disp.output}</span>}
            </>
          ) : (
            '—'
          )}
        </dd>
        <dt>{t('devices.mon.network')}</dt>
        <dd>
          <NetworkValue network={m.network} />
        </dd>
        <dt>{t('client.status.cpu')}</dt>
        <dd>{formatPercentValue(m.cpu)}</dd>
        <dt>{t('client.status.memory')}</dt>
        <dd>
          {m.ramTotal
            ? t('media.storage.diskLine', {
                used: formatBytes(m.ramUsed ?? 0),
                total: formatBytes(m.ramTotal),
              })
            : '—'}
        </dd>
        <dt>{t('client.status.temperature')}</dt>
        <dd>{formatTemperature(m.temperature)}</dd>
        {m.power && (
          <>
            <dt>{t('devices.mon.power')}</dt>
            <dd>
              {m.power.underVoltage
                ? t('devices.mon.powerLowNow', { count: m.power.events })
                : m.power.events > 0
                  ? t('devices.mon.powerEvents', { count: m.power.events })
                  : t('devices.mon.powerOk')}
            </dd>
          </>
        )}
        {m.video && (
          <>
            <dt>{t('devices.hwVideo')}</dt>
            <dd>
              {m.video.gpu === 'lost'
                ? t('devices.mon.gpuLost')
                : m.video.fallback
                  ? t('devices.mon.videoFallback')
                  : !m.video.hw
                    ? t('devices.mon.videoSw')
                    : m.video.path
                      ? t('devices.mon.videoHw', { path: videoPathLabel(m.video.path) })
                      : t('devices.hw.on')}
              {!!m.video.gpuRecoveries && m.video.gpu !== 'lost' && (
                <div className="faint">
                  {t('devices.mon.gpuRecovered', {
                    count: m.video.gpuRecoveries,
                    time: m.video.gpuLostAt ? formatDate(m.video.gpuLostAt) : '—',
                  })}
                </div>
              )}
              {!!m.video.gpuCrashes && (
                <div className="faint">{t('devices.mon.gpuCrashes', { count: m.video.gpuCrashes })}</div>
              )}
            </dd>
          </>
        )}
        {m.clock && (
          <>
            <dt>{t('devices.clock.title')}</dt>
            <dd>
              {t(`devices.clock.${m.clock.source}`)}
              {m.clock.source !== 'local' && (
                <span className="faint"> · ±{Math.max(1, Math.round(m.clock.rttMs / 2))} ms</span>
              )}
            </dd>
          </>
        )}
        {m.model && (
          <>
            <dt>{t('devices.mon.hardware')}</dt>
            <dd>
              {m.model}
              {unsupportedBoard(m.model) && (
                <div>
                  <Badge tone="warning">{t('devices.mon.unsupported')}</Badge>{' '}
                  <span className="muted">{t('devices.mon.unsupportedPi3')}</span>
                </div>
              )}
            </dd>
          </>
        )}
        <dt>{t('client.status.uptime')}</dt>
        <dd>{formatUptime(m.uptime)}</dd>
        <dt>{t('client.status.ips')}</dt>
        <dd className="mono">{d.ips.length ? d.ips.join(', ') : '—'}</dd>
      </dl>
    </div>
  );
}

function LevelBadge({ level }: { level: DiskLevel }) {
  const t = useT();
  if (level === 'critical')
    return (
      <Badge tone="danger">
        <OctagonAlert size={12} aria-hidden /> {t('media.storage.level.critical')}
      </Badge>
    );
  if (level === 'warning')
    return (
      <Badge tone="warning">
        <AlertTriangle size={12} aria-hidden /> {t('media.storage.level.warning')}
      </Badge>
    );
  return (
    <Badge tone="success">
      <CheckCircle2 size={12} aria-hidden /> {t('media.storage.level.ok')}
    </Badge>
  );
}

/** Storage of the device with "Clean cache" (preview first). */
export function StorageCard({
  d,
  onChanged,
  control = true,
}: {
  d: Device;
  onChanged: () => void;
  control?: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const [cleaning, setCleaning] = useState(false);
  const s = storageOf(d.metrics);
  const onDone = useCallback(
    (freed: number) => {
      toast(t('devices.clean.toast', { size: formatBytes(freed) }));
      onChanged();
    },
    [toast, t, onChanged],
  );
  const usedPct = s ? (s.used / s.total) * 100 : 0;
  const bytes = (v: number | null): string => (v === null ? '—' : formatBytes(v));
  return (
    <div className="card">
      <h2>{t('client.status.storage')}</h2>
      {!s ? (
        <p className="muted">{t('devices.storage.unknown')}</p>
      ) : (
        <>
          <div
            className="st-meter"
            role="meter"
            aria-label={t('client.status.storage')}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(usedPct)}
          >
            <div className={`st-meter-fill ${s.level}`} style={{ width: `${Math.min(100, usedPct)}%` }} />
          </div>
          <div className="st-disk-line">
            <span>
              {t('media.storage.diskLine', { used: formatBytes(s.used), total: formatBytes(s.total) })} ·{' '}
              <strong>
                {t('media.storage.free', {
                  free: formatBytes(s.free),
                  percent: formatPercent(s.freePercent),
                })}
              </strong>
            </span>
            <LevelBadge level={s.level} />
          </div>
          <dl className="kv" style={{ marginTop: 12 }}>
            <dt>{t('devices.storage.total')}</dt>
            <dd>{formatBytes(s.total)}</dd>
            <dt>{t('devices.storage.used')}</dt>
            <dd>{formatBytes(s.used)}</dd>
            <dt>{t('devices.storage.cache')}</dt>
            <dd>{bytes(s.cache)}</dd>
            <dt>{t('devices.storage.logs')}</dt>
            <dd>{bytes(s.logs)}</dd>
            <dt>{t('devices.storage.free')}</dt>
            <dd>
              {formatBytes(s.free)} <span className="faint">{formatPercent(s.freePercent)}</span>
            </dd>
          </dl>
          {s.level !== 'ok' && (
            <div className={`dev-banner ${s.level === 'critical' ? 'danger' : 'warning'}`} role="alert">
              <AlertTriangle size={16} />
              <div className="dev-banner-body">
                {s.level === 'critical'
                  ? t('devices.storage.criticalText')
                  : t('devices.storage.warningText')}
              </div>
            </div>
          )}
        </>
      )}
      <div className="form-actions">
        <Button
          icon={<Eraser size={16} />}
          disabled={!d.online || !control}
          onClick={() => setCleaning(true)}
        >
          {t('devices.action.clean_cache')}
        </Button>
      </div>
      {!d.online && <p className="faint">{t('devices.offlineActions')}</p>}
      {cleaning && <CleanCacheDialog d={d} onClose={() => setCleaning(false)} onDone={onDone} />}
    </div>
  );
}
