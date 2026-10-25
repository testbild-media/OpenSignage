// Updates: running version, update source and check, releases with
// download and server installation (backup + automatic rollback), offline bundle upload,
// rolling client update (one screen per zone at a time), history and trusted keys.
import { useCallback, useEffect, useRef, useState } from 'react';
import { Download, KeyRound, Play, RefreshCw, Rocket, Square, Trash2, Upload } from 'lucide-react';
import { has } from '@opensignage/i18n';
import { del, get, patch, post, translateError, uploadWithProgress } from '../../lib/api';
import { type Query, useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, Checkbox, DataState, ErrorText, PageHeader, TextInput } from '../../components/ui';
import { visibleReleases } from './releases';
import '../devices/devices.css'; // .failed-log
import './updates.css';

interface StoredError {
  code: string;
  message?: string;
  params?: Record<string, unknown>;
}

interface Operation {
  kind: 'download' | 'upload' | 'install';
  version: string;
  step: string;
  done: number;
  total: number;
  startedAt: string;
  startedBy: string;
  runId?: string;
  state?: string;
}

interface Release {
  version: string;
  source: 'github' | 'upload';
  notes: string;
  size: number;
  publishedAt: string;
  state: 'available' | 'downloading' | 'ready' | 'failed';
  error: string;
  updatedAt: string;
  newer: boolean;
  current: boolean;
  packages: { os: string; arch: string; size: number }[];
  serverPackage: boolean;
}

interface Overview {
  version: string;
  platform: string;
  repo: string;
  repoSetting: string;
  defaultRepo: string;
  updateCheck: boolean;
  autoAlignVersions: boolean;
  check: {
    lastCheckAt: string;
    lastError: StoredError | null;
    latest: string;
    checking: boolean;
    rateLimitedUntil?: string;
  };
  serverUpdate: { possible: boolean; root?: string; reason?: string; reasonCode?: 'not_linux' | 'not_installed' | 'not_writable' };
  pendingUpdate: {
    state: string;
    from: string;
    to: string;
    attempts: number;
    startedAt: string;
    error: string;
  } | null;
  operation: Operation | null;
  releases: Release[];
}

interface Run {
  id: string;
  kind: 'server' | 'clients';
  version: string;
  state: 'running' | 'done' | 'failed' | 'rolled_back' | 'stopped';
  error: string;
  startedBy: string;
  startedAt: string;
  finishedAt: string;
  devices?: Record<string, number>;
}

interface DeviceUpdate {
  runId: string;
  state: string;
  error: string;
  updatedAt: string;
  sentAt: string;
  fromVersion: string;
  version: string;
}

interface Client {
  id: string;
  name: string;
  zoneId: string;
  zoneName: string;
  version: string;
  versionMatch: boolean;
  online: boolean;
  platform: string;
  packageAvailable: boolean;
  alignOnConnect: boolean;
  update: DeviceUpdate | null;
}

interface Clients {
  serverVersion: string;
  packagesAvailable: boolean;
  run: Run | null;
  devices: Client[];
  timeoutMinutes: number;
}

interface TrustedKey {
  id: string;
  key: string;
  builtin: boolean;
}

const IN_FLIGHT = ['waiting', 'sent', 'downloading', 'installing', 'installed'];

function formatBytes(n: number): string {
  if (!n) return '0 MB';
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  return `${(n / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

function formatTime(s: string): string {
  if (!s) return '—';
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

/** Errors stored by the server are JSON {code, message, params}; agents may send plain text. */
function useErrorText(): (raw: string | StoredError | null | undefined) => string {
  const t = useT();
  return useCallback(
    (raw) => {
      if (!raw) return '';
      let e: StoredError | null = null;
      if (typeof raw === 'string') {
        try {
          e = JSON.parse(raw) as StoredError;
        } catch {
          return has(`errors.${raw}`) ? t(`errors.${raw}`) : raw;
        }
      } else e = raw;
      if (!e?.code) return String(raw);
      return translateError(e.code, e.params ?? {}, e.message || e.code);
    },
    [t],
  );
}

function Progress({ done, total, label }: { done: number; total: number; label: string }) {
  const pct = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : null;
  return (
    <div
      className="upd-progress"
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct ?? undefined}
      aria-label={label}
    >
      <div className="upd-progress-head">
        <span>{label}</span>
        <span className="faint">
          {pct === null ? '' : `${formatBytes(done)} / ${formatBytes(total)} · ${pct} %`}
        </span>
      </div>
      <div className={`upd-bar ${pct === null ? 'upd-bar-indeterminate' : ''}`}>
        <div style={{ width: pct === null ? undefined : `${pct}%` }} />
      </div>
    </div>
  );
}

export function UpdatesPage() {
  const t = useT();
  const ov = useQuery<Overview>('/api/v1/updates');
  const clients = useQuery<Clients>('/api/v1/updates/clients');
  const runs = useQuery<{ runs: Run[] }>('/api/v1/updates/runs');
  const [op, setOp] = useState<Operation | null>(null);
  const [restart, setRestart] = useState<{ from: string; to: string } | null>(null);
  const [rolledBack, setRolledBack] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => {
    setOp(ov.data?.operation ?? null);
  }, [ov.data]);

  const reloadAll = useCallback(() => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      ov.reload();
      clients.reload();
      runs.reload();
    }, 300);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ov.reload, clients.reload, runs.reload]);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  useEvents(['updates', 'devices', 'settings'], (e) => {
    if (e.topic === 'updates' && e.kind === 'operation') {
      const d = e.data as Operation;
      if (d.state === 'finished') {
        setOp(null);
        reloadAll();
      } else setOp(d);
      return;
    }
    if (e.topic === 'updates' && e.kind === 'server') {
      const d = e.data as { state: string; error?: string };
      if (d.state === 'failed') setRestart(null);
    }
    if (
      e.topic === 'devices' &&
      !['online', 'offline', 'claimed', 'deleted', 'updated', 'repaired', 'update_progress'].includes(
        e.kind ?? '',
      )
    )
      return;
    reloadAll();
  });

  // After the install request: wait until the server answers again, then reload (new
  // version) or report the automatic rollback (old version came back).
  useEffect(() => {
    if (!restart) return;
    let stop = false;
    let down = false;
    void (async () => {
      for (let i = 0; i < 300 && !stop; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        if (stop) return;
        try {
          const info = await get<{ version: string }>('/api/v1/system/info');
          if (info.version === restart.to) {
            location.reload();
            return;
          }
          if (down && info.version === restart.from) {
            setRestart(null);
            setRolledBack(restart.to);
            reloadAll();
            return;
          }
        } catch {
          down = true;
        }
      }
    })();
    return () => {
      stop = true;
    };
  }, [restart, reloadAll]);

  const data = ov.data;
  return (
    <div className="page page-narrow updates-page">
      <PageHeader
        title={t('updates.title')}
        subtitle={data ? t('updates.runningVersion', { version: data.version }) : undefined}
      />
      {restart && (
        <div className="card upd-notice" role="status">
          <h2>{t('updates.restartingTitle', { version: restart.to })}</h2>
          <p className="muted">{t('updates.restartingText')}</p>
          <Progress
            done={0}
            total={0}
            label={op?.step ? t(`updates.step.${op.step}`) : t('updates.step.restarting')}
          />
        </div>
      )}
      {rolledBack && (
        <div className="card upd-alert" role="alert">
          <h2>{t('updates.rolledBackTitle', { version: rolledBack })}</h2>
          <p className="muted">{t('updates.rolledBackText')}</p>
        </div>
      )}
      <DataState loading={ov.loading && !data} error={ov.error} onRetry={ov.reload}>
        {data && (
          <>
            <StatusCard data={data} />
            <SourceCard data={data} onChanged={ov.reload} />
            <ReleasesCard
              data={data}
              op={op}
              onChanged={reloadAll}
              onInstall={(to) => setRestart({ from: data.version, to })}
              busy={!!restart}
            />
            <UploadCard op={op} onChanged={reloadAll} />
          </>
        )}
      </DataState>
      <ClientsCard q={clients} onChanged={reloadAll} />
      <HistoryCard q={runs} />
      <KeysCard />
    </div>
  );
}

function StatusCard({ data }: { data: Overview }) {
  const t = useT();
  const errText = useErrorText();
  const p = data.pendingUpdate;
  return (
    <div className="card">
      <h2>{t('updates.status')}</h2>
      <dl className="kv">
        <dt>{t('updates.version')}</dt>
        <dd className="mono">{data.version}</dd>
        <dt>{t('updates.platform')}</dt>
        <dd className="mono">{data.platform}</dd>
        <dt>{t('updates.serverUpdate')}</dt>
        <dd>
          {data.serverUpdate.possible ? (
            <Badge tone="success">{t('updates.serverUpdatePossible')}</Badge>
          ) : (
            <>
              <Badge tone="warning">{t('updates.serverUpdateImpossible')}</Badge>
              {data.serverUpdate.reasonCode && <div>{t(`updates.serverReason.${data.serverUpdate.reasonCode}`)}</div>}
              <div className="faint mono">{data.serverUpdate.reason}</div>
            </>
          )}
        </dd>
        {p && (
          <>
            <dt>{t('updates.pending')}</dt>
            <dd>
              {p.state === 'rolled_back' ? (
                <Badge tone="danger">{t('updates.pendingRolledBack', { from: p.from, to: p.to })}</Badge>
              ) : (
                <Badge tone="warning">{t('updates.pendingRunning', { from: p.from, to: p.to })}</Badge>
              )}
              {p.error && <div className="faint">{errText(p.error)}</div>}
            </dd>
          </>
        )}
      </dl>
    </div>
  );
}

function SourceCard({ data, onChanged }: { data: Overview; onChanged: () => void }) {
  const t = useT();
  const toast = useToast();
  const errText = useErrorText();
  const [repo, setRepo] = useState(data.repoSetting);
  const [daily, setDaily] = useState(data.updateCheck);
  useEffect(() => {
    setRepo(data.repoSetting);
    setDaily(data.updateCheck);
  }, [data.repoSetting, data.updateCheck]);
  const save = useAction(async () => {
    await patch('/api/v1/settings', { updateRepo: repo.trim(), updateCheck: daily });
    toast(t('admin.saved'));
    onChanged();
  });
  const check = useAction(async () => {
    await post('/api/v1/updates/check');
    onChanged();
  });
  const dirty = repo.trim() !== data.repoSetting || daily !== data.updateCheck;
  const c = data.check;
  return (
    <div className="card form">
      <h2>{t('updates.source')}</h2>
      <TextInput
        label={t('updates.repo')}
        hint={t('updates.repoHint', { repo: data.defaultRepo })}
        placeholder={data.defaultRepo}
        value={repo}
        onChange={(e) => setRepo(e.target.value)}
        spellCheck={false}
        autoComplete="off"
      />
      <Checkbox label={t('updates.dailyCheck')} checked={daily} onChange={setDaily} />
      <ErrorText error={save.error} />
      <div className="form-actions">
        <Button variant="primary" disabled={!dirty} busy={save.busy} onClick={() => void save.run()}>
          {t('common.action.save')}
        </Button>
      </div>
      <div className="upd-check">
        <Button
          icon={<RefreshCw size={16} />}
          busy={check.busy || c.checking}
          disabled={dirty}
          onClick={() => void check.run()}
        >
          {t('updates.checkNow')}
        </Button>
        <div>
          <div className="muted">
            {t('updates.activeRepo')}: <span className="mono">{data.repo}</span>
          </div>
          <div className="faint">
            {c.lastCheckAt
              ? t('updates.lastCheck', { time: formatTime(c.lastCheckAt) })
              : t('updates.neverChecked')}
          </div>
          {c.lastError && <div className="form-error">{errText(c.lastError)}</div>}
          {!c.lastError && c.lastCheckAt && (
            <div className="faint">
              {c.latest ? t('updates.latestFound', { version: c.latest }) : t('updates.upToDate')}
            </div>
          )}
        </div>
      </div>
      <ErrorText error={check.error} />
    </div>
  );
}

function ReleasesCard({
  data,
  op,
  onChanged,
  onInstall,
  busy,
}: {
  data: Overview;
  op: Operation | null;
  onChanged: () => void;
  onInstall: (version: string) => void;
  busy: boolean;
}) {
  const t = useT();
  const confirm = useConfirm();
  const toast = useToast();
  const errText = useErrorText();
  const [rolloutClients, setRolloutClients] = useState(true);
  const download = useAction(async (v: string) => {
    await post(`/api/v1/updates/releases/${encodeURIComponent(v)}/download`);
    onChanged();
  });
  const remove = useAction(async (r: Release) => {
    const ok = await confirm({
      title: t('updates.deleteFiles'),
      message: t('updates.deleteFilesConfirm', { version: r.version }),
      confirmLabel: t('updates.deleteFiles'),
      danger: true,
    });
    if (!ok) return;
    await del(`/api/v1/updates/releases/${encodeURIComponent(r.version)}`);
    toast(t('updates.filesDeleted'));
    onChanged();
  });
  const install = useAction(async (r: Release) => {
    const ok = await confirm({
      title: t('updates.installTitle', { version: r.version }),
      message: (
        <div className="upd-confirm">
          <p>{t('updates.installText', { from: data.version, to: r.version })}</p>
          <ol className="steps">
            <li>{t('updates.installStepBackup')}</li>
            <li>{t('updates.installStepRestart')}</li>
            <li>{t('updates.installStepRollback')}</li>
            {rolloutClients && <li>{t('updates.installStepClients')}</li>}
          </ol>
        </div>
      ),
      confirmLabel: t('updates.install'),
      danger: true,
    });
    if (!ok) return;
    await post('/api/v1/updates/server/install', { version: r.version, rolloutClients });
    onInstall(r.version);
  });
  const anyOp = !!op;
  const shown = visibleReleases(data.releases, op?.kind === 'download' ? op.version : undefined);
  const hidden = data.releases.length - shown.length;
  return (
    <div className="card">
      <h2>{t('updates.releases')}</h2>
      {data.releases.length === 0 && <p className="muted">{t('updates.noReleases')}</p>}
      {data.releases.length > 0 && shown.length === 0 && <p className="muted">{t('updates.upToDate')}</p>}
      {shown.map((r) => {
        const downloading = op?.kind === 'download' && op.version === r.version;
        const installing = op?.kind === 'install' && op.version === r.version;
        const canInstall = r.state === 'ready' && r.newer && r.serverPackage && data.serverUpdate.possible;
        return (
          <div key={r.version} className="upd-release">
            <div className="upd-release-head">
              <span className="upd-release-version mono">{r.version}</span>
              {r.newer && <Badge tone="primary">{t('updates.newer')}</Badge>}
              {r.current && <Badge>{t('updates.currentVersion')}</Badge>}
              <Badge tone={r.state === 'ready' ? 'success' : r.state === 'failed' ? 'danger' : 'neutral'}>
                {t(`updates.relState.${r.state}`)}
              </Badge>
              <span className="faint">
                {t(`updates.relSource.${r.source}`)}
                {r.publishedAt && ` · ${formatTime(r.publishedAt)}`}
                {r.size > 0 && ` · ${formatBytes(r.size)}`}
              </span>
            </div>
            {r.current && <p className="faint">{t('updates.currentHint')}</p>}
            {r.notes && <pre className="upd-notes">{r.notes}</pre>}
            {r.packages.length > 0 && (
              <div className="chips">
                {r.packages.map((p) => (
                  <span key={`${p.os}/${p.arch}`} className="chip">
                    {p.os}/{p.arch} · {formatBytes(p.size)}
                  </span>
                ))}
              </div>
            )}
            {r.error && <div className="form-error">{errText(r.error)}</div>}
            {(downloading || installing) && op && (
              <Progress done={op.done} total={op.total} label={t(`updates.step.${op.step || 'metadata'}`)} />
            )}
            {r.state === 'ready' && r.newer && !r.serverPackage && (
              <p className="faint">{t('updates.noServerPackage', { platform: data.platform })}</p>
            )}
            {r.state === 'ready' && r.newer && r.serverPackage && !data.serverUpdate.possible && (
              <p className="faint">{t('updates.installNotPossible')}</p>
            )}
            <div className="form-actions upd-actions">
              {r.source === 'github' && (r.state === 'available' || r.state === 'failed') && (
                <Button
                  icon={<Download size={16} />}
                  disabled={anyOp}
                  busy={download.busy || downloading}
                  onClick={() => void download.run(r.version)}
                >
                  {r.state === 'failed' ? t('updates.retryDownload') : t('updates.download')}
                </Button>
              )}
              {(r.state === 'ready' || r.state === 'failed') && (
                <Button
                  variant="ghost"
                  icon={<Trash2 size={16} />}
                  disabled={anyOp || busy}
                  busy={remove.busy}
                  onClick={() => void remove.run(r)}
                >
                  {t('updates.deleteFiles')}
                </Button>
              )}
              {canInstall && (
                <>
                  <Checkbox
                    label={t('updates.rolloutAfter')}
                    checked={rolloutClients}
                    onChange={setRolloutClients}
                  />
                  <Button
                    variant="primary"
                    icon={<Rocket size={16} />}
                    disabled={anyOp || busy || !!data.pendingUpdate}
                    busy={install.busy || installing}
                    onClick={() => void install.run(r)}
                  >
                    {t('updates.install')}
                  </Button>
                </>
              )}
            </div>
          </div>
        );
      })}
      {hidden > 0 && <p className="faint">{t('updates.olderHidden', { count: hidden })}</p>}
      <ErrorText error={download.error ?? remove.error ?? install.error} />
    </div>
  );
}

function UploadCard({ op, onChanged }: { op: Operation | null; onChanged: () => void }) {
  const t = useT();
  const toast = useToast();
  const fileRef = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [sent, setSent] = useState<{ loaded: number; total: number } | null>(null);
  const upload = useAction(async () => {
    if (!file) return;
    setSent({ loaded: 0, total: file.size });
    try {
      const res = await uploadWithProgress<{ version: string }>(
        '/api/v1/updates/upload',
        file,
        (loaded, total) => setSent({ loaded, total }),
        'application/x-tar',
      );
      toast(t('updates.uploaded', { version: res.version }));
      setFile(null);
      if (fileRef.current) fileRef.current.value = '';
      onChanged();
    } finally {
      setSent(null);
    }
  });
  const verifying = op?.kind === 'upload' && op.step === 'verifying';
  return (
    <div className="card form">
      <h2>{t('updates.offline')}</h2>
      <p className="muted">{t('updates.offlineText')}</p>
      <input
        ref={fileRef}
        type="file"
        accept=".tar"
        disabled={upload.busy}
        onChange={(e) => setFile(e.target.files?.[0] ?? null)}
      />
      {sent &&
        (verifying || sent.loaded >= sent.total ? (
          <Progress done={0} total={0} label={t('updates.step.verifying')} />
        ) : (
          <Progress done={sent.loaded} total={sent.total} label={t('updates.step.receiving')} />
        ))}
      <ErrorText error={upload.error} />
      <div className="form-actions">
        <Button
          variant="primary"
          icon={<Upload size={16} />}
          disabled={!file || (!!op && !upload.busy)}
          busy={upload.busy}
          onClick={() => void upload.run()}
        >
          {t('updates.upload')}
        </Button>
      </div>
    </div>
  );
}

function ClientsCard({ q, onChanged }: { q: Query<Clients>; onChanged: () => void }) {
  const t = useT();
  const confirm = useConfirm();
  const errText = useErrorText();
  const start = useAction(async () => {
    const ok = await confirm({
      title: t('updates.rolloutStart'),
      message: t('updates.rolloutStartConfirm', { version: q.data?.serverVersion ?? '' }),
      confirmLabel: t('updates.rolloutStart'),
    });
    if (!ok) return;
    await post('/api/v1/updates/clients/start');
    onChanged();
  });
  const stop = useAction(async () => {
    await post('/api/v1/updates/clients/stop');
    onChanged();
  });
  const align = useAction(async (c: Client) => {
    await post(`/api/v1/devices/${encodeURIComponent(c.id)}/align`);
    onChanged();
  });
  const d = q.data;
  const running = d?.run?.state === 'running';
  const mismatching = d?.devices.filter((c) => !c.versionMatch).length ?? 0;
  return (
    <div className="card">
      <h2>{t('updates.clients')}</h2>
      <p className="muted">{t('updates.clientsText', { minutes: d?.timeoutMinutes ?? 10 })}</p>
      <DataState loading={q.loading && !d} error={q.error} onRetry={q.reload}>
        {d && (
          <>
            {!d.packagesAvailable && mismatching > 0 && (
              <div className="form-error">{t('updates.noClientPackages', { version: d.serverVersion })}</div>
            )}
            {d.run && (
              <div className="upd-run">
                <Badge
                  tone={
                    running
                      ? 'primary'
                      : d.run.state === 'done'
                        ? 'success'
                        : d.run.state === 'stopped'
                          ? 'neutral'
                          : 'danger'
                  }
                >
                  {t(`updates.runState.${d.run.state}`)}
                </Badge>
                <span className="muted">
                  {t('updates.runInfo', {
                    version: d.run.version,
                    by: d.run.startedBy || '—',
                    time: formatTime(d.run.startedAt),
                  })}
                </span>
                {d.run.error && <div className="form-error">{errText(d.run.error)}</div>}
              </div>
            )}
            {(d.devices.length > 0 || running) && (
              <div className="form-actions upd-actions">
                {running ? (
                  <Button
                    variant="danger"
                    icon={<Square size={16} />}
                    busy={stop.busy}
                    onClick={() => void stop.run()}
                  >
                    {t('updates.rolloutStop')}
                  </Button>
                ) : (
                  <Button
                    variant="primary"
                    icon={<Play size={16} />}
                    disabled={mismatching === 0 || !d.packagesAvailable}
                    busy={start.busy}
                    onClick={() => void start.run()}
                  >
                    {t('updates.rolloutStart')}
                  </Button>
                )}
                <span className="faint">
                  {mismatching === 0
                    ? t('updates.allAligned')
                    : t('updates.mismatching', { count: mismatching })}
                </span>
              </div>
            )}
            <ErrorText error={start.error ?? stop.error ?? align.error} />
            {d.devices.length === 0 ? (
              <p className="muted">{t('updates.noDevices')}</p>
            ) : (
              <div className="upd-table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>{t('updates.col.screen')}</th>
                      <th>{t('updates.col.zone')}</th>
                      <th>{t('updates.col.version')}</th>
                      <th>{t('updates.col.state')}</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {d.devices.map((c) => {
                      const u = c.update;
                      const busyDev =
                        !!u &&
                        IN_FLIGHT.includes(u.state) &&
                        u.version === d.serverVersion &&
                        d.run?.id === u.runId &&
                        running;
                      return (
                        <tr key={c.id}>
                          <td>
                            <div>{c.name}</div>
                            <div className="faint">
                              {c.online ? t('updates.isOnline') : t('updates.isOffline')}
                              {c.platform && ` · ${c.platform}`}
                            </div>
                          </td>
                          <td>{c.zoneName || <span className="faint">{t('updates.noZone')}</span>}</td>
                          <td className="mono">
                            {c.versionMatch ? c.version : <Badge tone="warning">{c.version || '—'}</Badge>}
                          </td>
                          <td>
                            {u ? (
                              <>
                                <Badge
                                  tone={
                                    u.state === 'done'
                                      ? 'success'
                                      : ['failed', 'rolled_back'].includes(u.state)
                                        ? 'danger'
                                        : IN_FLIGHT.includes(u.state)
                                          ? 'primary'
                                          : 'neutral'
                                  }
                                >
                                  {t(`updates.duState.${u.state}`)}
                                </Badge>
                                <div className="faint">
                                  {t('updates.toVersion', {
                                    version: u.version,
                                    time: formatTime(u.updatedAt),
                                  })}
                                </div>
                                {u.error && <div className="form-error upd-small">{errText(u.error)}</div>}
                                <FailedLog log={storedLog(u.error)} />
                              </>
                            ) : (
                              <span className="faint">—</span>
                            )}
                          </td>
                          <td className="row-actions">
                            {!c.versionMatch && !busyDev && (
                              <Button
                                disabled={!c.online || !c.packageAvailable}
                                title={
                                  !c.online
                                    ? t('updates.alignOffline')
                                    : !c.packageAvailable
                                      ? t('updates.alignNoPackage')
                                      : undefined
                                }
                                busy={align.busy}
                                onClick={() => void align.run(c)}
                              >
                                {t('updates.align')}
                              </Button>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
      </DataState>
    </div>
  );
}

/** The log a screen sent with a failed update (params.log of the stored error). */
function storedLog(raw: string): string {
  try {
    const e = JSON.parse(raw) as StoredError;
    return typeof e.params?.log === 'string' ? e.params.log : '';
  } catch {
    return '';
  }
}

/** The log of a failed version, folded (it can be long). */
function FailedLog({ log }: { log: string | undefined }) {
  const t = useT();
  if (!log) return null;
  return (
    <details className="failed-log">
      <summary>{t('devices.alerts.log')}</summary>
      <pre>{log}</pre>
    </details>
  );
}

// The history shows the newest runs; older ones on request.
const HISTORY_SHOWN = 10;

function HistoryCard({ q }: { q: Query<{ runs: Run[] }> }) {
  const t = useT();
  const errText = useErrorText();
  const [all, setAll] = useState(false);
  const runs = q.data?.runs ?? [];
  const list = all ? runs : runs.slice(0, HISTORY_SHOWN);
  return (
    <div className="card">
      <h2>{t('updates.history')}</h2>
      <DataState
        loading={q.loading && !q.data}
        error={q.error}
        onRetry={q.reload}
        empty={list.length === 0}
        emptyText={t('updates.noHistory')}
      >
        <div className="upd-table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{t('updates.col.started')}</th>
                <th>{t('updates.col.kind')}</th>
                <th>{t('updates.col.version')}</th>
                <th>{t('updates.col.state')}</th>
                <th>{t('updates.col.by')}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((r) => (
                <tr key={r.id}>
                  <td className="nowrap">{formatTime(r.startedAt)}</td>
                  <td>{t(`updates.kind.${r.kind}`)}</td>
                  <td className="mono">{r.version}</td>
                  <td>
                    <Badge
                      tone={
                        r.state === 'done'
                          ? 'success'
                          : r.state === 'running'
                            ? 'primary'
                            : r.state === 'stopped'
                              ? 'neutral'
                              : 'danger'
                      }
                    >
                      {t(`updates.runState.${r.state}`)}
                    </Badge>
                    {r.kind === 'clients' && r.devices && Object.keys(r.devices).length > 0 && (
                      <div className="faint">
                        {Object.entries(r.devices)
                          .map(([s, n]) => `${t(`updates.duState.${s}`)}: ${n}`)
                          .join(' · ')}
                      </div>
                    )}
                    {r.error && <div className="form-error upd-small">{errText(r.error)}</div>}
                  </td>
                  <td>{r.startedBy || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!all && runs.length > HISTORY_SHOWN && (
          <div className="form-actions">
            <Button variant="ghost" onClick={() => setAll(true)}>
              {t('updates.historyOlder', { count: runs.length - HISTORY_SHOWN })}
            </Button>
          </div>
        )}
      </DataState>
    </div>
  );
}

function KeysCard() {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const q = useQuery<{ keys: TrustedKey[] }>('/api/v1/updates/keys');
  const [key, setKey] = useState('');
  const add = useAction(async () => {
    q.setData(await post<{ keys: TrustedKey[] }>('/api/v1/updates/keys', { key: key.trim() }));
    setKey('');
    toast(t('updates.keyAdded'));
  });
  const remove = useAction(async (k: TrustedKey) => {
    const ok = await confirm({
      title: t('updates.keyRemove'),
      message: t('updates.keyRemoveConfirm'),
      confirmLabel: t('updates.keyRemove'),
      danger: true,
    });
    if (!ok) return;
    q.setData(await del<{ keys: TrustedKey[] }>(`/api/v1/updates/keys/${encodeURIComponent(k.id)}`));
  });
  return (
    <div className="card form">
      <h2>{t('updates.keys')}</h2>
      <p className="muted">{t('updates.keysText')}</p>
      <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload}>
        <ul className="upd-keys">
          {q.data?.keys.map((k) => (
            <li key={k.id}>
              <KeyRound size={16} />
              <span className="mono upd-key">{k.key}</span>
              {k.builtin ? (
                <Badge>{t('updates.keyBuiltin')}</Badge>
              ) : (
                <Button
                  variant="ghost"
                  icon={<Trash2 size={16} />}
                  busy={remove.busy}
                  onClick={() => void remove.run(k)}
                >
                  {t('updates.keyRemove')}
                </Button>
              )}
            </li>
          ))}
        </ul>
      </DataState>
      <TextInput
        label={t('updates.keyAdd')}
        placeholder="osk1:…"
        value={key}
        onChange={(e) => setKey(e.target.value)}
        spellCheck={false}
        autoComplete="off"
      />
      <ErrorText error={add.error ?? remove.error} />
      <div className="form-actions">
        <Button variant="primary" disabled={!key.trim()} busy={add.busy} onClick={() => void add.run()}>
          {t('updates.keyAddButton')}
        </Button>
      </div>
    </div>
  );
}
