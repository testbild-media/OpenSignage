// Storage dashboard: disk usage with warning levels, breakdown,
// largest files, size per folder and the trash retention.
import { Link } from 'react-router';
import { AlertTriangle, CheckCircle2, Clock, OctagonAlert, RefreshCw } from 'lucide-react';
import { useQuery, useT } from '../../lib/hooks';
import { Badge, Button, DataState, PageHeader } from '../../components/ui';
import { KindIcon } from '../../components/MediaPicker';
import { folderRoute, formatBytes, formatPercent, type MediaKind } from '../media/model';
import { useMediaReload } from '../media/TrashPage';

interface StorageResponse {
  disk: {
    path: string;
    totalBytes: number;
    usedBytes: number;
    freeBytes: number;
    freePercent: number;
    level: 'ok' | 'warning' | 'critical';
  };
  breakdown: Record<string, number>;
  largest: {
    id: string;
    name: string;
    kind: MediaKind;
    size: number;
    folderId: string | null;
    inTrash: boolean;
  }[];
  folders: { id: string; name: string; path: string[]; size: number; count: number }[];
  root: { size: number; count: number };
}

const CATEGORIES = [
  'images',
  'videoOriginals',
  'videoVariants',
  'audio',
  'thumbnails',
  'fonts',
  'trash',
  'unreferenced',
  'database',
  'logs',
  'tmp',
];

function Level({ level }: { level: StorageResponse['disk']['level'] }) {
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

export function StoragePage() {
  const t = useT();
  const q = useQuery<StorageResponse>('/api/v1/storage');
  const settings = useQuery<{ trashRetentionDays: number }>('/api/v1/settings');
  useMediaReload(q.reload);
  const s = q.data;
  const d = s?.disk;
  const usedPct = d && d.totalBytes ? (d.usedBytes / d.totalBytes) * 100 : 0;
  const rows = s
    ? CATEGORIES.map((c) => ({ key: c, size: s.breakdown[c] ?? 0 })).sort((a, b) => b.size - a.size)
    : [];
  const dataTotal = rows.reduce((sum, r) => sum + r.size, 0);
  const max = Math.max(1, ...rows.map((r) => r.size));
  const pathOf = (folderId: string | null): string => {
    if (!folderId) return t('media.root');
    const f = s?.folders.find((x) => x.id === folderId);
    return f ? [t('media.root'), ...f.path].join(' › ') : '…';
  };
  const retention = settings.data?.trashRetentionDays;
  return (
    <div className="page">
      <PageHeader
        title={t('admin.settings.storage')}
        actions={
          <Button icon={<RefreshCw size={16} />} busy={q.loading && !!q.data} onClick={q.reload}>
            {t('media.storage.refresh')}
          </Button>
        }
      />
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload}>
        {s && d && (
          <>
            <div className="card">
              <h2>{t('media.storage.disk')}</h2>
              {d.totalBytes === 0 ? (
                <div className="muted">{t('media.storage.diskUnknown')}</div>
              ) : (
                <>
                  <div
                    className="st-meter"
                    role="meter"
                    aria-label={t('media.storage.disk')}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={Math.round(usedPct)}
                    title={t('media.storage.diskLine', {
                      used: formatBytes(d.usedBytes),
                      total: formatBytes(d.totalBytes),
                    })}
                  >
                    <div
                      className={`st-meter-fill ${d.level}`}
                      style={{ width: `${Math.min(100, usedPct)}%` }}
                    />
                  </div>
                  <div className="st-disk-line">
                    <span>
                      {t('media.storage.diskLine', {
                        used: formatBytes(d.usedBytes),
                        total: formatBytes(d.totalBytes),
                      })}{' '}
                      ·{' '}
                      <strong>
                        {t('media.storage.free', {
                          free: formatBytes(d.freeBytes),
                          percent: formatPercent(d.freePercent),
                        })}
                      </strong>
                    </span>
                    <Level level={d.level} />
                  </div>
                  {d.level !== 'ok' && (
                    <div
                      className={`mm-banner ${d.level === 'critical' ? 'danger' : 'warning'}`}
                      role="alert"
                      style={{ marginTop: 12, marginBottom: 0 }}
                    >
                      <AlertTriangle size={16} />
                      <div className="mm-banner-body">
                        {d.level === 'critical'
                          ? t('media.storage.criticalText')
                          : t('media.storage.warningText')}{' '}
                        <Link to="/media/unused">{t('admin.nav.mediaUnused')}</Link> ·{' '}
                        <Link to="/media/trash">{t('admin.nav.mediaTrash')}</Link>
                      </div>
                    </div>
                  )}
                </>
              )}
              <div className="faint mono" style={{ marginTop: 8 }}>
                {d.path}
              </div>
            </div>

            <div className="card">
              <h2>{t('media.storage.breakdown')}</h2>
              <p className="muted" style={{ marginTop: 0 }}>
                {t('media.storage.breakdownTotal', { size: formatBytes(dataTotal) })}
              </p>
              <div className="st-bars">
                {rows.map((r) => {
                  const share = dataTotal ? (r.size / dataTotal) * 100 : 0;
                  const label = t(`media.storage.cat.${r.key}`);
                  return (
                    <div
                      key={r.key}
                      className={`st-bar-row ${r.size ? '' : 'zero'}`}
                      title={`${label}: ${formatBytes(r.size)} (${formatPercent(share)})`}
                    >
                      <span className="st-bar-label">{label}</span>
                      <span className="st-bar-track">
                        <span className="st-bar" style={{ width: `${(r.size / max) * 100}%` }} />
                      </span>
                      <span className="st-bar-value">
                        {formatBytes(r.size)} <span className="faint">{formatPercent(share)}</span>
                      </span>
                    </div>
                  );
                })}
              </div>
              <p className="faint">{t('media.storage.unreferencedHint')}</p>
            </div>

            <div className="card">
              <h2>{t('media.storage.trash')}</h2>
              <div className="mm-inline-status">
                <Clock size={16} aria-hidden />
                <span>
                  {retention === undefined
                    ? '…'
                    : retention > 0
                      ? t('media.trash.retention', { days: retention })
                      : t('media.trash.retentionNever')}
                </span>
                <Link to="/settings/defaults">{t('media.storage.changeRetention')}</Link>
                <span className="faint">·</span>
                <Link to="/media/trash">
                  {t('media.storage.openTrash', { size: formatBytes(s.breakdown.trash ?? 0) })}
                </Link>
                <span className="faint">·</span>
                <Link to="/media/unused">{t('admin.nav.mediaUnused')}</Link>
              </div>
            </div>

            <div className="card">
              <h2>{t('media.storage.largest')}</h2>
              {s.largest.length === 0 ? (
                <div className="muted">{t('media.storage.noFiles')}</div>
              ) : (
                <table className="table">
                  <thead>
                    <tr>
                      <th>{t('media.trash.name')}</th>
                      <th>{t('media.details.location')}</th>
                      <th>{t('media.storage.footprint')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {s.largest.map((a) => (
                      <tr key={a.id}>
                        <td>
                          <Link
                            className="mm-name-cell"
                            to={
                              a.inTrash
                                ? '/media/trash'
                                : `${folderRoute(a.folderId)}?asset=${encodeURIComponent(a.id)}`
                            }
                          >
                            <KindIcon kind={a.kind} size={14} />
                            <span>{a.name}</span>
                          </Link>
                        </td>
                        <td className="muted">{a.inTrash ? t('media.trash.title') : pathOf(a.folderId)}</td>
                        <td className="nowrap">{formatBytes(a.size)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              <p className="faint">{t('media.storage.footprintHint')}</p>
            </div>

            <div className="card">
              <h2>{t('media.storage.folders')}</h2>
              <table className="table">
                <thead>
                  <tr>
                    <th>{t('media.storage.folder')}</th>
                    <th>{t('media.storage.files')}</th>
                    <th>{t('media.details.size')}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <td>
                      <Link to="/media">{t('media.storage.rootFiles')}</Link>
                    </td>
                    <td>{s.root.count}</td>
                    <td className="nowrap">{formatBytes(s.root.size)}</td>
                  </tr>
                  {s.folders.map((f) => (
                    <tr key={f.id}>
                      <td>
                        <Link to={folderRoute(f.id)}>{[t('media.root'), ...f.path].join(' › ')}</Link>
                      </td>
                      <td>{f.count}</td>
                      <td className="nowrap">{formatBytes(f.size)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="faint">{t('media.storage.foldersHint')}</p>
            </div>
          </>
        )}
      </DataState>
    </div>
  );
}
