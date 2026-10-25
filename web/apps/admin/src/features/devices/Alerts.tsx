// Problems of paired devices: the list behind the sidebar marker and
// the dashboard figure, and the compact markers of the device list. A problem can be
// acknowledged: it then no longer counts as open until it is gone and comes back (server
// devices/acks.go); acknowledged problems are listed separately and can be shown again.
import { Link } from 'react-router';
import { AlertTriangle, Check, Eye, OctagonAlert } from 'lucide-react';
import { has } from '@opensignage/i18n';
import { post, translateError } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { Badge, Button, DataState, ErrorText, PageHeader } from '../../components/ui';
import { formatDate } from '../media/model';
import { formatPercentValue, groupAlerts, openAlerts, type Alert } from './model';

export interface AlertsResponse {
  count: number;
  alerts: Alert[];
}

/** Loads the alerts and keeps them current (device and update events, every 30 s). */
export function useAlertsQuery(enabled = true) {
  const q = useQuery<AlertsResponse>(enabled ? '/api/v1/alerts' : null);
  useEvents(['devices', 'updates'], () => q.reload());
  return q;
}

/** The problem as a short text, with its detail (player state, free space, version). */
export function useAlertText(): (a: Alert) => string {
  const t = useT();
  return (a) => {
    const label = has(`devices.alert.${a.kind}`) ? t(`devices.alert.${a.kind}`) : a.kind;
    switch (a.kind) {
      case 'disk_low':
      case 'disk_critical': {
        const pct = Number(a.detail);
        return a.detail && Number.isFinite(pct)
          ? `${label} · ${t('devices.alerts.diskFree', { percent: formatPercentValue(pct) })}`
          : label;
      }
      case 'version_mismatch':
        return a.detail ? `${label} · ${a.detail}` : label;
      case 'update_failed': {
        const head = a.version ? `${label} · ${a.version}` : label;
        return a.detail ? `${head} · ${translateError(a.detail, {}, a.detail)}` : head;
      }
      case 'sync_failed':
        return a.detail
          ? `${label} · ${has(`client.syncError.${a.detail}`) ? t(`client.syncError.${a.detail}`) : a.detail}`
          : label;
      default:
        return label;
    }
  };
}

function SeverityIcon({ severity }: { severity: Alert['severity'] }) {
  return severity === 'critical' ? (
    <OctagonAlert size={16} className="sev-critical" aria-hidden />
  ) : (
    <AlertTriangle size={16} className="sev-warning" aria-hidden />
  );
}

/** Acknowledges a problem or shows it again; onDone reloads the list. */
function useAck(onDone?: () => void) {
  return useAction(async (a: Alert, on: boolean) => {
    await post(on ? '/api/v1/alerts/ack' : '/api/v1/alerts/unack', { deviceId: a.deviceId, kind: a.kind });
    onDone?.();
  });
}

/** Open problems grouped by device: device (link), problem, since, "Acknowledge". */
export function AlertList({ alerts, onChanged }: { alerts: Alert[]; onChanged?: () => void }) {
  const t = useT();
  const text = useAlertText();
  const ack = useAck(onChanged);
  const groups = groupAlerts(openAlerts(alerts));
  return (
    <>
      <table className="table alert-table">
        <thead>
          <tr>
            <th>{t('devices.alerts.device')}</th>
            <th>{t('devices.alerts.problem')}</th>
            <th>{t('devices.alerts.since')}</th>
            <th aria-label={t('devices.alerts.ack')} />
          </tr>
        </thead>
        <tbody>
          {groups.flatMap((g) =>
            g.alerts.map((a, i) => (
              <tr key={`${g.deviceId}-${a.kind}`} className={i > 0 ? 'alert-cont' : ''}>
                <td>
                  {i === 0 && (
                    <>
                      <Link to={`/devices/${g.deviceId}`}>{g.deviceName}</Link>
                      {g.zoneName && <div className="faint">{g.zoneName}</div>}
                    </>
                  )}
                </td>
                <td>
                  <span className="alert-problem">
                    <SeverityIcon severity={a.severity} />
                    <span>{text(a)}</span>
                  </span>
                  {a.log && (
                    <details className="failed-log">
                      <summary>{t('devices.alerts.log')}</summary>
                      <pre>{a.log}</pre>
                    </details>
                  )}
                </td>
                <td className="nowrap faint">{a.since ? formatDate(a.since) : '—'}</td>
                <td className="alert-ack">
                  <Button
                    variant="ghost"
                    icon={<Check size={14} />}
                    busy={ack.busy}
                    title={t('devices.alerts.ackTitle')}
                    onClick={() => void ack.run(a, true)}
                  >
                    {t('devices.alerts.ack')}
                  </Button>
                </td>
              </tr>
            )),
          )}
        </tbody>
      </table>
      <ErrorText error={ack.error} />
    </>
  );
}

/** Acknowledged problems: device, problem, who acknowledged it when, "Show again". */
export function AckedList({ alerts, onChanged }: { alerts: Alert[]; onChanged?: () => void }) {
  const t = useT();
  const text = useAlertText();
  const ack = useAck(onChanged);
  const acked = alerts.filter((a) => a.ack);
  return (
    <>
      <table className="table alert-table alert-acked">
        <thead>
          <tr>
            <th>{t('devices.alerts.device')}</th>
            <th>{t('devices.alerts.problem')}</th>
            <th>{t('devices.alerts.acknowledged')}</th>
            <th aria-label={t('devices.alerts.unack')} />
          </tr>
        </thead>
        <tbody>
          {acked.map((a) => (
            <tr key={`${a.deviceId}-${a.kind}`}>
              <td>
                <Link to={`/devices/${a.deviceId}`}>{a.deviceName}</Link>
                {a.zoneName && <div className="faint">{a.zoneName}</div>}
              </td>
              <td>
                <span className="alert-problem">
                  <SeverityIcon severity={a.severity} />
                  <span>{text(a)}</span>
                </span>
              </td>
              <td className="faint">
                {t('devices.alerts.ackedBy', { user: a.ack!.by || '—', time: formatDate(a.ack!.at) })}
              </td>
              <td className="alert-ack">
                <Button
                  variant="ghost"
                  icon={<Eye size={14} />}
                  busy={ack.busy}
                  onClick={() => void ack.run(a, false)}
                >
                  {t('devices.alerts.unack')}
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <ErrorText error={ack.error} />
    </>
  );
}

/** Page behind the sidebar marker and the dashboard figure. */
export function AlertsPage() {
  const t = useT();
  const q = useAlertsQuery();
  const all = q.data?.alerts ?? [];
  const open = openAlerts(all);
  const acked = all.length - open.length;
  return (
    <div className="page page-narrow">
      <PageHeader title={t('admin.dashboard.alerts')} subtitle={t('devices.alerts.subtitle')} />
      <DataState
        loading={q.loading && !q.data}
        error={q.error}
        onRetry={q.reload}
        empty={all.length === 0}
        emptyText={t('devices.alerts.empty')}
      >
        <div className="card">
          {open.length > 0 ? (
            <AlertList alerts={open} onChanged={q.reload} />
          ) : (
            <p className="faint">{t('devices.alerts.noneOpen')}</p>
          )}
        </div>
        {acked > 0 && (
          <div className="card">
            <h2>{t('devices.alerts.ackedTitle', { count: acked })}</h2>
            <p className="faint">{t('devices.alerts.ackedHint')}</p>
            <AckedList alerts={all} onChanged={q.reload} />
          </div>
        )}
      </DataState>
    </div>
  );
}

/** Compact problem markers of one device (device list, detail header). Acknowledged
 * problems are left out, or shown muted with showAcked (device page). */
export function ProblemBadges({
  alerts,
  showAcked = false,
}: {
  alerts: Alert[] | undefined;
  showAcked?: boolean;
}) {
  const t = useT();
  const list = showAcked ? (alerts ?? []) : openAlerts(alerts);
  if (!list.length) return null;
  const label = (a: Alert): string =>
    has(`devices.alert.${a.kind}`) ? t(`devices.alert.${a.kind}`) : a.kind;
  return (
    <span className="problem-badges">
      {list.map((a) =>
        a.ack ? (
          <span
            key={a.kind}
            title={t('devices.alerts.ackedBy', { user: a.ack.by || '—', time: formatDate(a.ack.at) })}
          >
            <Badge>
              <Check size={12} aria-hidden /> {label(a)} · {t('devices.alerts.ackedBadge')}
            </Badge>
          </span>
        ) : (
          <Badge key={a.kind} tone={a.severity === 'critical' ? 'danger' : 'warning'}>
            {label(a)}
          </Badge>
        ),
      )}
    </span>
  );
}
