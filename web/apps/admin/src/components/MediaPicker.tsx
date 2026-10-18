// The one media folder browser: breadcrumb, folder tiles, search over
// all folders, letterboxed thumbnails (never cropped), live processing status. It serves
// the media manager, the editor pickers, the product image picker and the playlist sidebar.
// "root" is always sent explicitly, so "top level" is never confused with "no filter".
import {
  type HTMLAttributes,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import {
  ChevronRight,
  Film,
  Folder,
  Home,
  ImageOff,
  Image as ImageIcon,
  Music,
  Search,
  Upload,
  X,
} from 'lucide-react';
import { has } from '@opensignage/i18n';
import { ApiError } from '../lib/api';
import { type Query, useQuery, useT } from '../lib/hooks';
import { useEvents } from '../lib/events';
import { Badge, Button, DataState, IconButton } from './ui';
import {
  ACCEPT,
  type Crumb,
  fileUrl,
  isPickable,
  formatBytes,
  formatDuration,
  listingPath,
  type MediaAsset as FullAsset,
  type MediaFolder,
  type MediaKind as Kind,
  type MediaListing,
  type MediaProgress,
  ROOT,
} from '../features/media/model';
import { enqueueUploads, useUploadDone } from '../features/media/queue';
import { UploadList } from '../features/media/UploadPanel';
import { useRights } from '../lib/rights';
import './controls.css';
import './media-picker.css';
import '../features/media/media.css';

export type MediaKind = Kind;
export type MediaAsset = FullAsset;

const ALL_KINDS: MediaKind[] = ['image', 'video', 'audio'];

export function mediaThumbUrl(assetId: string): string {
  return fileUrl(assetId, 'thumb');
}

export function KindIcon({ kind, size = 16 }: { kind: MediaKind; size?: number }) {
  const Icon = kind === 'video' ? Film : kind === 'audio' ? Music : ImageIcon;
  return <Icon size={size} aria-hidden />;
}

/**
 * Letterboxed thumbnail of an asset with a neutral fallback when it cannot be loaded.
 * `version` changes the URL (e.g. revision + status) so a new thumbnail is fetched after
 * processing or a replacement.
 */
export function MediaThumb({
  assetId,
  kind = 'image',
  alt = '',
  className,
  version,
  badge,
}: {
  assetId: string;
  kind?: MediaKind;
  alt?: string;
  className?: string;
  version?: string | number;
  badge?: ReactNode;
}) {
  const [failed, setFailed] = useState<string | null>(null);
  const src =
    version === undefined
      ? mediaThumbUrl(assetId)
      : `${mediaThumbUrl(assetId)}&v=${encodeURIComponent(String(version))}`;
  const broken = failed === src;
  return (
    <span className={`media-thumb ${className ?? ''}`}>
      {broken || kind === 'audio' ? (
        <span className="media-thumb-fallback" aria-hidden="true">
          {kind === 'audio' ? (
            <Music size={22} />
          ) : kind === 'video' ? (
            <Film size={22} />
          ) : (
            <ImageOff size={22} />
          )}
        </span>
      ) : (
        <img src={src} alt={alt} loading="lazy" draggable={false} onError={() => setFailed(src)} />
      )}
      {badge}
    </span>
  );
}

/** Thumbnail of a listed asset: refreshed after processing, with kind/duration badge. */
export function AssetThumb({ asset, badge = true }: { asset: MediaAsset; badge?: boolean }) {
  const duration = asset.meta?.durationMs;
  return (
    <MediaThumb
      assetId={asset.id}
      kind={asset.kind}
      version={`${asset.revision}.${asset.status === 'ready' ? 1 : 0}`}
      badge={
        badge && asset.kind !== 'image' ? (
          <span className="mm-thumb-badge">
            <KindIcon kind={asset.kind} size={11} />
            {duration ? formatDuration(duration) : null}
          </span>
        ) : null
      }
    />
  );
}

export function assetSummary(a: MediaAsset): string {
  const parts = [formatBytes(a.size)];
  if (a.meta?.width && a.meta.height && a.kind !== 'audio') parts.push(`${a.meta.width}×${a.meta.height}`);
  if (a.meta?.durationMs && a.kind !== 'image') parts.push(formatDuration(a.meta.durationMs));
  return parts.join(' · ');
}

function overallPercent(p: MediaProgress | undefined): number | null {
  if (!p || !p.total) return null;
  const done = Math.max(0, (p.index || 1) - 1) + Math.min(100, Math.max(0, p.percent)) / 100;
  return Math.min(100, Math.round((done / p.total) * 100));
}

/** Processing state: progress while processing, error badge (and reprocess) when failed. */
export function AssetStatus({
  asset,
  onReprocess,
  busy,
}: {
  asset: MediaAsset;
  onReprocess?: () => void;
  busy?: boolean;
}) {
  const t = useT();
  if (asset.status === 'processing') {
    const pct = overallPercent(asset.progress);
    const step = asset.progress?.step;
    const stepLabel =
      step && has(`media.step.${step}`) ? t(`media.step.${step}`) : t('media.status.processing');
    return (
      <>
        <div
          className={`mm-progress ${pct === null ? 'indeterminate' : ''}`}
          role="progressbar"
          aria-label={t('media.status.processing')}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct ?? undefined}
        >
          <div style={pct === null ? undefined : { width: `${pct}%` }} />
        </div>
        <span className="faint nowrap" title={stepLabel}>
          {pct === null ? t('media.status.processing') : `${pct} %`}
        </span>
      </>
    );
  }
  if (asset.status === 'failed') {
    return (
      <>
        <Badge tone="danger">{t('media.status.failed')}</Badge>
        {onReprocess && (
          <Button className="btn-sm" busy={busy} onClick={onReprocess}>
            {t('media.reprocess')}
          </Button>
        )}
      </>
    );
  }
  return null;
}

export function Breadcrumb({
  path,
  onOpen,
  crumbProps,
}: {
  path: Crumb[];
  onOpen: (folderId: string | null) => void;
  /** Extra props per crumb (drop targets in the manager). */
  crumbProps?: (folderId: string | null) => HTMLAttributes<HTMLButtonElement> & { className?: string };
}) {
  const t = useT();
  const items: { id: string | null; name: string }[] = [
    { id: null, name: t('media.root') },
    ...path.filter((c) => c.id && c.id !== ROOT),
  ];
  return (
    <nav className="media-crumbs" aria-label={t('media.breadcrumb')}>
      {items.map((c, i) => {
        const last = i === items.length - 1;
        const { className: extraClass, ...rest } = crumbProps?.(c.id) ?? {};
        return (
          <span key={c.id ?? ROOT} className="media-crumb-wrap">
            {i > 0 && <ChevronRight size={14} className="faint" aria-hidden />}
            <button
              type="button"
              className={`media-crumb ${last ? 'current' : ''} ${extraClass ?? ''}`}
              aria-current={last ? 'page' : undefined}
              onClick={() => onOpen(c.id)}
              {...rest}
            >
              {i === 0 && <Home size={14} aria-hidden />}
              <span>{c.name}</span>
            </button>
          </span>
        );
      })}
    </nav>
  );
}

export function FolderTile({
  folder,
  onOpen,
  actions,
  tileProps,
  className,
}: {
  folder: MediaFolder;
  onOpen: () => void;
  actions?: ReactNode;
  tileProps?: HTMLAttributes<HTMLDivElement>;
  className?: string;
}) {
  const t = useT();
  return (
    <div className={`mm-tile mm-folder ${className ?? ''}`} {...tileProps}>
      <button type="button" className="mm-tile-main" onClick={onOpen} title={folder.name}>
        <Folder size={30} className="mm-folder-icon" aria-hidden />
        <span className="mm-folder-text">
          <span className="mm-tile-name">{folder.name}</span>
          <span className="mm-tile-sub">
            {t('media.folder.summary', { files: folder.itemCount ?? 0, size: formatBytes(folder.totalSize) })}
          </span>
        </span>
      </button>
      {actions && <div className="mm-tile-actions">{actions}</div>}
    </div>
  );
}

export function AssetTile({
  asset,
  onOpen,
  onDoubleClick,
  disabled,
  disabledReason,
  selected,
  check,
  actions,
  status,
  tileProps,
}: {
  asset: MediaAsset;
  onOpen: (e: ReactMouseEvent<HTMLButtonElement>) => void;
  onDoubleClick?: () => void;
  disabled?: boolean;
  disabledReason?: string;
  selected?: boolean;
  check?: { checked: boolean; onToggle: (e: ReactMouseEvent<HTMLInputElement>) => void };
  actions?: ReactNode;
  status?: ReactNode;
  tileProps?: HTMLAttributes<HTMLDivElement>;
}) {
  const t = useT();
  return (
    <div className={`mm-tile ${selected ? 'selected' : ''} ${disabled ? 'disabled' : ''}`} {...tileProps}>
      {check && (
        <label className={`mm-check ${check.checked ? '' : 'idle'}`}>
          <input
            type="checkbox"
            checked={check.checked}
            onChange={() => undefined}
            onClick={check.onToggle}
            aria-label={t('media.selectOne', { name: asset.name })}
          />
        </label>
      )}
      <button
        type="button"
        className="mm-tile-main"
        onClick={onOpen}
        onDoubleClick={onDoubleClick}
        disabled={disabled}
        aria-pressed={selected}
        title={disabled && disabledReason ? disabledReason : asset.name}
      >
        <AssetThumb asset={asset} />
        <span className="mm-tile-name">{asset.name}</span>
        <span className="mm-tile-sub">{assetSummary(asset)}</span>
      </button>
      <div className="mm-tile-status">{status ?? <AssetStatus asset={asset} />}</div>
      {actions && <div className="mm-tile-actions">{actions}</div>}
    </div>
  );
}

/**
 * Loads a media listing and keeps it current: processing progress is merged in place,
 * every other media event and every finished upload reloads (debounced).
 */
export function useMediaListing(path: string | null): Query<MediaListing> {
  const q = useQuery<MediaListing>(path);
  const [progress, setProgress] = useState<Record<string, MediaProgress>>({});
  const timer = useRef<number | undefined>(undefined);
  const { reload } = q;
  const reloadSoon = useCallback(() => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(reload, 250);
  }, [reload]);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  useEffect(() => setProgress({}), [q.data]);
  useEvents(['media'], (e) => {
    const d = (e.data ?? {}) as {
      status?: string;
      step?: string;
      index?: number;
      total?: number;
      percent?: number;
    };
    const id = e.id;
    if (e.kind === 'progress') {
      // Progress of files that are not listed here (other folders) needs no reload.
      const shown = q.data?.assets.find((a) => a.id === id);
      if (!id || !shown) return;
      if (d.status === 'processing' && shown.status === 'processing') {
        setProgress((p) => ({
          ...p,
          [id]: { step: d.step ?? '', index: d.index ?? 0, total: d.total ?? 0, percent: d.percent ?? 0 },
        }));
        return;
      }
    }
    reloadSoon();
  });
  useUploadDone(reloadSoon);
  const data = useMemo(() => {
    if (!q.data || !Object.keys(progress).length) return q.data;
    return {
      ...q.data,
      assets: q.data.assets.map((a) => (progress[a.id] ? { ...a, progress: progress[a.id] } : a)),
    };
  }, [q.data, progress]);
  return { ...q, data };
}

export interface MediaBrowserProps {
  folder: string;
  onFolderChange: (folder: string) => void;
  /** Allowed asset kinds (all when omitted); other assets are not shown. Folders always are. */
  kinds?: MediaKind[];
  layout?: 'tiles' | 'list';
  /** Show the search box (searches all folders). */
  search?: boolean;
  /**
   * Search term typed into the caller's own search box (searches all folders like the
   * built-in box, which is then hidden). Opening a folder calls onFolderChange as usual.
   */
  query?: string;
  /** Offer an "All / Images / Videos" filter when several kinds are allowed. */
  kindFilter?: boolean;
  /** Click on an asset (tiles) or Enter/click (list). */
  onActivate?: (a: MediaAsset) => void;
  /** Double click on an asset. */
  onPick?: (a: MediaAsset) => void;
  /** Only these assets can be activated (default: ready ones). */
  canPick?: (a: MediaAsset) => boolean;
  /** Extra props per asset row/tile (drag source, keyboard) . */
  assetProps?: (a: MediaAsset) => HTMLAttributes<HTMLElement>;
  selectedId?: string;
  actions?: ReactNode;
}

/** Folder browser for choosing media: real click-through navigation, never a flat list. */
export function MediaBrowser({
  folder,
  onFolderChange,
  kinds = ALL_KINDS,
  layout = 'tiles',
  search = true,
  query,
  kindFilter = true,
  onActivate,
  onPick,
  canPick = isPickable,
  assetProps,
  selectedId,
  actions,
}: MediaBrowserProps) {
  const t = useT();
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState<'all' | MediaKind>('all');
  const term = query !== undefined ? query.trim() : search ? q.trim() : '';
  const shown = filter === 'all' ? kinds : kinds.filter((k) => k === filter);
  const list = useMediaListing(
    listingPath({ folder, q: term, kind: shown.length === 1 ? shown[0] : undefined }),
  );
  const assets = (list.data?.assets ?? []).filter((a) => shown.includes(a.kind));
  const folders = list.data?.folders ?? [];
  const open = (id: string | null): void => {
    setQ('');
    onFolderChange(id ?? ROOT);
  };
  const reasonFor = (a: MediaAsset): string =>
    a.status === 'failed' ? t('media.status.failedHint') : t('media.status.processingHint');
  const folderGone = list.error instanceof ApiError && list.error.status === 404 && folder !== ROOT && !term;
  return (
    <div className="mm-browser">
      <div className="mm-toolbar mm-browser-bar">
        {term ? (
          <div className="mm-note">{t('media.searchResults', { q: term })}</div>
        ) : (
          <Breadcrumb path={list.data?.breadcrumb ?? []} onOpen={open} />
        )}
        <div className="mm-tools">
          {kindFilter && kinds.length > 1 && (
            <div className="segmented" role="group" aria-label={t('media.kind.label')}>
              {(['all', ...kinds] as const).map((k) => (
                <button
                  key={k}
                  type="button"
                  className={filter === k ? 'active' : ''}
                  aria-pressed={filter === k}
                  onClick={() => setFilter(k)}
                >
                  {t(`media.kind.plural.${k}`)}
                </button>
              ))}
            </div>
          )}
          {search && query === undefined && (
            <label className="media-search">
              <Search size={14} aria-hidden />
              <input
                type="search"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder={t('media.search')}
                aria-label={t('media.search')}
              />
            </label>
          )}
          {actions}
        </div>
      </div>
      {folderGone ? (
        <div className="state state-empty">
          <div>{t('media.folder.gone')}</div>
          <Button onClick={() => open(null)}>{t('media.backToRoot')}</Button>
        </div>
      ) : (
        <DataState
          loading={list.loading && !list.data}
          error={list.error}
          onRetry={list.reload}
          empty={!!list.data && folders.length + assets.length === 0}
          emptyText={term ? t('media.noResults') : t('media.emptyPick')}
        >
          {layout === 'tiles' ? (
            <div className="mm-grid mm-grid-sm" aria-busy={list.loading || undefined}>
              {folders.map((f) => (
                <FolderTile key={f.id} folder={f} onOpen={() => open(f.id)} />
              ))}
              {assets.map((a) => {
                const ok = canPick(a);
                return (
                  <AssetTile
                    key={a.id}
                    asset={a}
                    selected={a.id === selectedId}
                    disabled={!ok}
                    disabledReason={reasonFor(a)}
                    onOpen={() => ok && onActivate?.(a)}
                    onDoubleClick={() => ok && onPick?.(a)}
                    tileProps={assetProps?.(a) as HTMLAttributes<HTMLDivElement> | undefined}
                  />
                );
              })}
            </div>
          ) : (
            <div className="mm-list">
              {folders.map((f) => (
                <button type="button" key={f.id} className="mm-row" onClick={() => open(f.id)} title={f.name}>
                  <Folder size={15} className="mm-folder-icon" aria-hidden />
                  <span className="mm-row-name">{f.name}</span>
                  <ChevronRight size={14} aria-hidden />
                </button>
              ))}
              {assets.map((a) => {
                const ok = canPick(a);
                return (
                  <div
                    key={a.id}
                    className={`mm-row ${ok ? '' : 'faint'}`}
                    role="button"
                    tabIndex={0}
                    aria-disabled={!ok || undefined}
                    title={ok ? a.name : reasonFor(a)}
                    onClick={ok && onActivate ? () => onActivate(a) : undefined}
                    onDoubleClick={ok && onPick ? () => onPick(a) : undefined}
                    onKeyDown={(e) => {
                      if (ok && e.key === 'Enter') (onPick ?? onActivate)?.(a);
                    }}
                    {...(ok ? assetProps?.(a) : {})}
                  >
                    <AssetThumb asset={a} badge={false} />
                    <span className="mm-row-name">{a.name}</span>
                    {a.status !== 'ready' && (
                      <Badge tone={a.status === 'failed' ? 'danger' : 'warning'}>
                        {t(`media.status.${a.status}`)}
                      </Badge>
                    )}
                  </div>
                );
              })}
            </div>
          )}
          {!!list.data && list.data.total > list.data.assets.length && (
            <div className="faint pad">
              {t('media.truncated', { shown: list.data.assets.length, total: list.data.total })}
            </div>
          )}
        </DataState>
      )}
    </div>
  );
}

/**
 * Dialog for choosing one asset. Click selects, double click or "Choose" picks; with
 * pickOnClick a single click picks. Files can be uploaded into the open folder.
 */
export function MediaPickerDialog({
  kinds = ALL_KINDS,
  title,
  onPick,
  onClose,
  pickOnClick = false,
}: {
  /** Allowed kinds; other assets are not shown. */
  kinds?: MediaKind[];
  title?: string;
  onPick: (asset: MediaAsset) => void;
  onClose: () => void;
  pickOnClick?: boolean;
}) {
  const t = useT();
  const [folder, setFolder] = useState(ROOT);
  const [selected, setSelected] = useState<MediaAsset | null>(null);
  const [keys, setKeys] = useState<number[]>([]);
  const canUpload = useRights().canMedia('upload');
  const ref = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  // The picker is usually opened from another dialog: Escape must close only the picker.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape') return;
      e.stopPropagation();
      e.preventDefault();
      closeRef.current();
    };
    window.addEventListener('keydown', onKey, true);
    ref.current?.focus();
    return () => window.removeEventListener('keydown', onKey, true);
  }, []);

  const heading = title ?? t('media.pick.title');
  const ready = (a: MediaAsset | null): a is MediaAsset => !!a && isPickable(a);
  return (
    <div
      className="dialog-backdrop media-picker-backdrop"
      onMouseDown={(e) => e.target === e.currentTarget && onClose()}
    >
      <div
        className="dialog dialog-wide media-picker"
        role="dialog"
        aria-modal="true"
        aria-label={heading}
        ref={ref}
        tabIndex={-1}
      >
        <div className="dialog-head">
          <h2>{heading}</h2>
          <IconButton label={t('common.action.close')} onClick={onClose}>
            <X size={18} />
          </IconButton>
        </div>
        <div className="dialog-body media-picker-body">
          <UploadList keys={keys} inline />
          <MediaBrowser
            folder={folder}
            onFolderChange={(f) => {
              setFolder(f);
              setSelected(null);
            }}
            kinds={kinds}
            selectedId={selected?.id}
            onActivate={(a) => (pickOnClick ? onPick(a) : setSelected(a))}
            onPick={onPick}
            actions={
              <>
                <input
                  ref={input}
                  type="file"
                  multiple
                  hidden
                  accept={kinds.map((k) => ACCEPT[k]).join(',')}
                  onChange={(e) => {
                    const files = [...(e.target.files ?? [])];
                    e.target.value = '';
                    if (files.length) {
                      const added = enqueueUploads(files, folder === ROOT ? null : folder);
                      setKeys((k) => [...k, ...added]);
                    }
                  }}
                />
                {canUpload && (
                  <Button
                    className="btn-sm"
                    icon={<Upload size={14} />}
                    onClick={() => input.current?.click()}
                  >
                    {t('media.upload.here')}
                  </Button>
                )}
              </>
            }
          />
        </div>
        <div className="dialog-foot">
          <span className="media-picker-selection ellipsis muted">{selected?.name ?? ''}</span>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          {!pickOnClick && (
            <Button
              variant="primary"
              disabled={!ready(selected)}
              onClick={() => ready(selected) && onPick(selected)}
            >
              {t('media.pick.choose')}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
