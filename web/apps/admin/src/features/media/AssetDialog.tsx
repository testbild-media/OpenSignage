// Details of one asset: full (never cropped) preview, metadata, usage, revisions and
// variants; rename with explicit save, replace keeping the id, move, reprocess, trash.
import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router';
import {
  Download,
  FolderInput,
  ListVideo,
  Music,
  Package,
  Presentation as PresIcon,
  Replace,
  Trash2,
} from 'lucide-react';
import { has } from '@opensignage/i18n';
import { patch, post, del } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, TextInput } from '../../components/ui';
import { useMediaRights } from './rights';
import { AssetStatus } from '../../components/MediaPicker';
import {
  ACCEPT,
  type AssetDetails,
  failureText,
  fileUrl,
  formatBytes,
  formatDate,
  formatDuration,
  groupUsage,
  hasPreview,
  type MediaProgress,
  mediaErrorText,
  pathText,
  ROOT,
} from './model';
import { enqueueUploads, useUploadDone } from './queue';
import { UploadList } from './UploadPanel';
import { FolderChooserDialog, validName } from './FolderDialogs';

function Preview({ asset }: { asset: AssetDetails }) {
  const t = useT();
  const [failed, setFailed] = useState(false);
  const version = `${asset.revision}.${asset.status}`;
  const [seen, setSeen] = useState(version);
  if (seen !== version) {
    setSeen(version);
    setFailed(false);
  }
  const src = `${fileUrl(asset.id, 'display')}&v=${encodeURIComponent(version)}`;
  const poster = asset.variants.includes('poster')
    ? `${fileUrl(asset.id, 'poster')}&v=${encodeURIComponent(version)}`
    : undefined;
  if (failed || !hasPreview(asset)) {
    return (
      <div className="mm-preview-empty">
        {poster && <img src={poster} alt="" />}
        <div>
          {asset.status === 'processing' ? t('media.details.previewLater') : t('media.details.noPreview')}
        </div>
      </div>
    );
  }
  if (asset.kind === 'image')
    return <img key={version} src={src} alt={asset.name} onError={() => setFailed(true)} />;
  if (asset.kind === 'video')
    return (
      <video
        key={version}
        src={src}
        poster={poster}
        controls
        preload="metadata"
        onError={() => setFailed(true)}
      />
    );
  return (
    <div className="mm-preview-audio">
      <Music size={48} aria-hidden />
      <audio key={version} src={src} controls preload="metadata" onError={() => setFailed(true)} />
    </div>
  );
}

function RenameField({
  asset,
  onSaved,
  readOnly,
}: {
  asset: AssetDetails;
  onSaved: () => void;
  readOnly?: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const [name, setName] = useState(asset.name);
  const [base, setBase] = useState(asset.name);
  if (base !== asset.name) {
    // renamed elsewhere: follow unless the user is editing
    setBase(asset.name);
    if (name === base) setName(asset.name);
  }
  const save = useAction(async () => {
    await patch(`/api/v1/media/${encodeURIComponent(asset.id)}`, { name: name.trim() });
    toast(t('media.renamed'));
    onSaved();
  });
  const valid = validName(name);
  const changed = name.trim() !== asset.name;
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    if (valid && changed) void save.run();
  };
  return (
    <form onSubmit={submit}>
      <div className="mm-rename">
        <TextInput
          label={t('media.details.name')}
          value={name}
          disabled={readOnly}
          maxLength={120}
          onChange={(e) => {
            setName(e.target.value);
            save.clearError();
          }}
          error={name.trim() && !valid ? t('media.nameRule') : null}
        />
        {!readOnly && (
          <Button
            type="submit"
            variant={changed ? 'primary' : 'secondary'}
            busy={save.busy}
            disabled={!valid || !changed}
          >
            {t('common.action.save')}
          </Button>
        )}
      </div>
      {changed && valid && <div className="field-hint">{t('media.details.renameHint')}</div>}
      {!!save.error && (
        <div className="form-error" role="alert">
          {mediaErrorText(save.error)}
        </div>
      )}
    </form>
  );
}

const USAGE_ICON = { presentation: PresIcon, playlist: ListVideo, product: Package, other: Package };

function usageLink(type: string, id: string): string | null {
  if (type === 'presentation') return `/presentations/${encodeURIComponent(id)}`;
  if (type === 'playlist') return `/playlists/${encodeURIComponent(id)}`;
  if (type === 'product') return '/products';
  return null;
}

export function AssetDialog({
  id,
  onClose,
  onChanged,
}: {
  id: string;
  onClose: () => void;
  onChanged: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const mr = useMediaRights();
  const q = useQuery<AssetDetails>(`/api/v1/media/${encodeURIComponent(id)}`);
  const [progress, setProgress] = useState<MediaProgress | undefined>(undefined);
  const [moving, setMoving] = useState(false);
  const [replaceKeys, setReplaceKeys] = useState<number[]>([]);
  const input = useRef<HTMLInputElement>(null);
  const timer = useRef<number | undefined>(undefined);
  const { reload } = q;
  const reloadSoon = useCallback(() => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(reload, 250);
  }, [reload]);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  useEffect(() => setProgress(undefined), [q.data]);
  useEvents(['media'], (e) => {
    if (e.id && e.id !== id && e.topic !== '*') return;
    const d = (e.data ?? {}) as {
      status?: string;
      step?: string;
      index?: number;
      total?: number;
      percent?: number;
    };
    if (e.kind === 'progress' && d.status === 'processing' && q.data?.status === 'processing') {
      setProgress({ step: d.step ?? '', index: d.index ?? 0, total: d.total ?? 0, percent: d.percent ?? 0 });
      return;
    }
    reloadSoon();
  });
  useUploadDone((item) => {
    if (item.replaceAssetId === id) reloadSoon();
  });

  const changed = useCallback(() => {
    q.reload();
    onChanged();
  }, [q, onChanged]);

  const reprocess = useAction(async () => {
    await post(`/api/v1/media/${encodeURIComponent(id)}/reprocess`);
    toast(t('media.reprocessStarted'));
    changed();
  });
  const trash = useAction(async () => {
    const d = q.data;
    if (!d) return;
    const used = groupUsage(d.usage).length;
    const ok = await confirm({
      title: t('media.trash.titleOne'),
      message: used
        ? t('media.trash.confirmUsed', { name: d.name, count: used })
        : t('media.trash.confirmOne', { name: d.name }),
      confirmLabel: t('media.trash.action'),
      danger: true,
    });
    if (!ok) return;
    await del(`/api/v1/media/${encodeURIComponent(id)}`);
    toast(t('media.trash.doneOne', { name: d.name }));
    onChanged();
    onClose();
  });
  const replace = async (file: File | undefined): Promise<void> => {
    const d = q.data;
    if (!file || !d) return;
    const ok = await confirm({
      title: t('media.replace.title'),
      message: t('media.replace.confirm', { name: d.name, file: file.name }),
      confirmLabel: t('media.replace.action'),
    });
    if (!ok) return;
    // enqueue outside the state updater: updaters run during rendering (twice in StrictMode)
    const added = enqueueUploads([file], null, id);
    setReplaceKeys((k) => [...k, ...added]);
  };
  const closeMove = useCallback(() => setMoving(false), []);

  const d = q.data ? { ...q.data, progress: progress ?? q.data.progress } : undefined;
  const failure = d?.status === 'failed' ? failureText(d.error) : null;
  const m = d?.meta ?? {};
  const usage = d ? groupUsage(d.usage) : [];
  return (
    <>
      <Dialog
        title={d?.name ?? t('media.details.title')}
        onClose={onClose}
        wide
        footer={
          d && (
            <>
              <a className="btn btn-secondary" href={fileUrl(d.id, 'orig', { download: true })} download>
                <Download size={16} />
                <span>{t('media.details.download')}</span>
              </a>
              <input
                ref={input}
                type="file"
                hidden
                accept={ACCEPT[d.kind]}
                onChange={(e) => void replace(e.target.files?.[0]).finally(() => (e.target.value = ''))}
              />
              {mr.ownAsset(d) && (
                <>
                  <Button icon={<Replace size={16} />} onClick={() => input.current?.click()}>
                    {t('media.replace.button')}
                  </Button>
                  <Button icon={<FolderInput size={16} />} onClick={() => setMoving(true)}>
                    {t('media.move.button')}
                  </Button>
                </>
              )}
              {mr.manage && (
                <Button
                  variant="danger"
                  icon={<Trash2 size={16} />}
                  busy={trash.busy}
                  onClick={() => void trash.run()}
                >
                  {t('media.trash.action')}
                </Button>
              )}
            </>
          )
        }
      >
        <DataState loading={q.loading && !q.data} error={q.error} onRetry={q.reload}>
          {d && (
            <div className="mm-detail">
              <div className="mm-preview">
                <Preview asset={d} />
              </div>
              <div className="mm-info">
                <UploadList keys={replaceKeys} inline />
                <RenameField asset={d} onSaved={changed} readOnly={!mr.ownAsset(d)} />
                {!!(trash.error ?? reprocess.error) && (
                  <div className="form-error" role="alert">
                    {mediaErrorText(trash.error ?? reprocess.error)}
                  </div>
                )}
                <section>
                  <h3>{t('media.details.location')}</h3>
                  <div className="mm-inline-status">
                    <span>{pathText(d.path)}</span>
                    <Link
                      className="btn btn-ghost btn-sm"
                      to={d.folderId ? `/media/folder/${encodeURIComponent(d.folderId)}` : '/media'}
                      onClick={onClose}
                    >
                      {t('media.details.openFolder')}
                    </Link>
                  </div>
                </section>
                <section>
                  <h3>{t('media.details.status')}</h3>
                  <div className="mm-inline-status">
                    {d.status === 'ready' && <Badge tone="success">{t('media.status.ready')}</Badge>}
                    {d.status !== 'ready' && <AssetStatus asset={d} />}
                    {d.status !== 'processing' && mr.manage && (
                      <Button className="btn-sm" busy={reprocess.busy} onClick={() => void reprocess.run()}>
                        {t('media.reprocess')}
                      </Button>
                    )}
                  </div>
                  {failure && (
                    <div className="field-error" style={{ marginTop: 6 }}>
                      {failure.text}
                      {failure.detail && <div className="faint mono">{failure.detail}</div>}
                    </div>
                  )}
                  {d.status === 'processing' && d.progress?.step && has(`media.step.${d.progress.step}`) && (
                    <div className="faint">{t(`media.step.${d.progress.step}`)}</div>
                  )}
                </section>
                <section>
                  <h3>{t('media.details.info')}</h3>
                  <dl className="mm-kv">
                    <dt>{t('media.details.type')}</dt>
                    <dd>
                      {t(`media.kind.${d.kind}`)} · <span className="mono">{d.mime || m.mime || '—'}</span>
                    </dd>
                    <dt>{t('media.details.size')}</dt>
                    <dd>{formatBytes(d.size)}</dd>
                    {!!m.width && !!m.height && (
                      <>
                        <dt>{t('media.details.dimensions')}</dt>
                        <dd>
                          {m.width} × {m.height} px
                        </dd>
                      </>
                    )}
                    {!!m.durationMs && (
                      <>
                        <dt>{t('media.details.duration')}</dt>
                        <dd>{formatDuration(m.durationMs)}</dd>
                      </>
                    )}
                    {!!m.fps && d.kind === 'video' && (
                      <>
                        <dt>{t('media.details.fps')}</dt>
                        <dd>{Math.round(m.fps * 100) / 100}</dd>
                      </>
                    )}
                    {d.kind !== 'image' && (m.videoCodec || m.audioCodec) && (
                      <>
                        <dt>{t('media.details.codecs')}</dt>
                        <dd className="mono">
                          {[m.videoCodec, m.pixFmt, m.audioCodec].filter(Boolean).join(' · ')}
                        </dd>
                      </>
                    )}
                    {d.kind === 'video' && d.status === 'ready' && !m.hasAudio && (
                      <>
                        <dt>{t('media.details.audio')}</dt>
                        <dd>{t('media.details.noAudio')}</dd>
                      </>
                    )}
                    {!!m.bitrate && (
                      <>
                        <dt>{t('media.details.bitrate')}</dt>
                        <dd>{Math.round(m.bitrate / 1000)} kbit/s</dd>
                      </>
                    )}
                    {(m.animated || m.hasAlpha) && (
                      <>
                        <dt>{t('media.details.features')}</dt>
                        <dd>
                          {[m.animated && t('media.details.animated'), m.hasAlpha && t('media.details.alpha')]
                            .filter(Boolean)
                            .join(' · ')}
                        </dd>
                      </>
                    )}
                    <dt>{t('media.details.uploaded')}</dt>
                    <dd>{formatDate(d.createdAt)}</dd>
                    <dt>{t('media.details.changed')}</dt>
                    <dd>{formatDate(d.updatedAt)}</dd>
                    <dt>{t('media.details.revision')}</dt>
                    <dd>{d.revision}</dd>
                    <dt>ID</dt>
                    <dd className="mono">{d.id}</dd>
                    <dt>SHA-256</dt>
                    <dd className="mono" title={d.sha256}>
                      {d.sha256.slice(0, 16)}…
                    </dd>
                  </dl>
                </section>
                <section>
                  <h3>{t('media.details.usage')}</h3>
                  {usage.length === 0 ? (
                    <div className="muted">{t('media.details.unused')}</div>
                  ) : (
                    <ul className="mm-usage">
                      {usage.map((u) => {
                        const Icon = USAGE_ICON[u.type];
                        const to = usageLink(u.type, u.id);
                        const label = u.name || u.id;
                        return (
                          <li key={`${u.type}:${u.id}`}>
                            <Icon size={14} aria-hidden />
                            <span className="faint">{t(`media.usage.${u.type}`)}</span>
                            {to ? (
                              <Link to={to} onClick={onClose}>
                                {label}
                              </Link>
                            ) : (
                              <span>{label}</span>
                            )}
                            {u.states.map((s) =>
                              has(`media.usage.state.${s}`) ? (
                                <Badge key={s}>{t(`media.usage.state.${s}`)}</Badge>
                              ) : null,
                            )}
                          </li>
                        );
                      })}
                    </ul>
                  )}
                </section>
                <section>
                  <h3>{t('media.details.revisions')}</h3>
                  <table className="mm-mini-table">
                    <thead>
                      <tr>
                        <th>#</th>
                        <th>{t('media.details.uploaded')}</th>
                        <th>{t('media.details.size')}</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {d.revisions.map((r) => (
                        <tr key={r.revision}>
                          <td>{r.revision}</td>
                          <td>{formatDate(r.createdAt)}</td>
                          <td>{formatBytes(r.size)}</td>
                          <td className="row-actions">
                            {r.current && <Badge tone="primary">{t('media.details.current')}</Badge>}{' '}
                            <a
                              href={fileUrl(d.id, 'orig', { rev: r.revision, download: true })}
                              download
                              title={t('media.details.downloadRevision', { revision: r.revision })}
                            >
                              <Download
                                size={14}
                                aria-label={t('media.details.downloadRevision', { revision: r.revision })}
                              />
                            </a>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </section>
                <section>
                  <h3>{t('media.details.variants')}</h3>
                  {d.variantInfo.length === 0 ? (
                    <div className="muted">
                      {d.status === 'processing'
                        ? t('media.details.variantsLater')
                        : t('media.details.noVariants')}
                    </div>
                  ) : (
                    <table className="mm-mini-table">
                      <thead>
                        <tr>
                          <th>{t('media.details.variant')}</th>
                          <th>#</th>
                          <th>{t('media.details.dimensions')}</th>
                          <th>{t('media.details.size')}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {d.variantInfo.map((v) => (
                          <tr
                            key={`${v.revision}:${v.variant}`}
                            className={v.revision === d.revision ? '' : 'faint'}
                          >
                            <td>
                              {has(`media.variant.${v.variant}`)
                                ? t(`media.variant.${v.variant}`)
                                : v.variant}
                            </td>
                            <td>{v.revision}</td>
                            <td>{v.width && v.height ? `${v.width} × ${v.height}` : '—'}</td>
                            <td>{formatBytes(v.size)}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  )}
                </section>
              </div>
            </div>
          )}
        </DataState>
      </Dialog>
      {moving && d && (
        <FolderChooserDialog
          title={t('media.move.titleOne', { name: d.name })}
          start={d.folderId ?? ROOT}
          source={d.folderId}
          onClose={closeMove}
          onConfirm={async (target) => {
            await patch(`/api/v1/media/${encodeURIComponent(d.id)}`, { folderId: target });
            toast(t('media.move.doneOne', { name: d.name }));
            setMoving(false);
            changed();
          }}
        />
      )}
    </>
  );
}
