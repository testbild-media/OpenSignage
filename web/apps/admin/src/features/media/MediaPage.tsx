// Media manager like a real file manager: folder tiles with breadcrumb,
// the top level shows only its own files, search spans all folders, uploads by button or
// drag & drop, multi-select, move by clicking through folders, delete = trash.
import {
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router';
import { FolderInput, FolderPlus, Pencil, Search, Trash2, Upload, X } from 'lucide-react';
import { ApiError, del, patch, post } from '../../lib/api';
import { useT } from '../../lib/hooks';
import { useConfirm, useToast } from '../../components/feedback';
import { Button, DataState, IconButton, PageHeader } from '../../components/ui';
import { useMediaRights } from './rights';
import {
  AssetStatus,
  AssetTile,
  Breadcrumb,
  FolderTile,
  useMediaListing,
} from '../../components/MediaPicker';
import {
  ACCEPT,
  folderRoute,
  formatBytes,
  listingPath,
  type MediaAsset,
  type MediaFolder,
  type MediaKind,
  mediaErrorText,
  ROOT,
} from './model';
import { enqueueUploads } from './queue';
import { UploadPanel } from './UploadPanel';
import { AssetDialog } from './AssetDialog';
import { FolderChooserDialog, NameDialog } from './FolderDialogs';

const PAGE = 500;
const MIME_ASSETS = 'application/x-os-media-assets';
const MIME_FOLDER = 'application/x-os-media-folder';

type Chooser =
  | { type: 'assets'; ids: string[]; source: string | null | undefined }
  | { type: 'folder'; folder: MediaFolder };
type Naming = { type: 'create' } | { type: 'rename'; folder: MediaFolder };

/** Runs fn for all items with limited parallelism; returns the failures. */
async function each<T>(list: T[], fn: (x: T) => Promise<unknown>, parallel = 4): Promise<unknown[]> {
  const errors: unknown[] = [];
  let i = 0;
  const worker = async (): Promise<void> => {
    while (i < list.length) {
      const x = list[i++]!;
      try {
        await fn(x);
      } catch (e) {
        errors.push(e);
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(parallel, list.length) }, worker));
  return errors;
}

const hasType = (e: DragEvent, type: string): boolean => Array.from(e.dataTransfer.types).includes(type);

export function MediaPage() {
  const t = useT();
  const toast = useToast();
  const mr = useMediaRights();
  const confirm = useConfirm();
  const navigate = useNavigate();
  const { id } = useParams();
  const folder = id ?? ROOT;
  const folderId = folder === ROOT ? null : folder;
  const [params, setParams] = useSearchParams();
  const openAsset = params.get('asset');

  const [q, setQ] = useState('');
  const [term, setTerm] = useState('');
  useEffect(() => {
    const timer = window.setTimeout(() => setTerm(q.trim()), 250);
    return () => clearTimeout(timer);
  }, [q]);
  const [kind, setKind] = useState<MediaKind | ''>('');
  const [limit, setLimit] = useState(PAGE);
  const list = useMediaListing(listingPath({ folder, q: term, kind, limit }));
  const folders = useMemo(() => list.data?.folders ?? [], [list.data]);
  const assets = useMemo(() => list.data?.assets ?? [], [list.data]);
  const crumbs = list.data?.breadcrumb ?? [];
  const here = folderId ? (crumbs.at(-1)?.name ?? '…') : t('media.root');

  // selection ---------------------------------------------------------------------------
  const [sel, setSel] = useState<Set<string>>(new Set());
  const anchor = useRef<string | null>(null);
  useEffect(() => {
    setSel((s) => {
      if (!s.size) return s;
      const ids = new Set(assets.map((a) => a.id));
      const next = new Set([...s].filter((x) => ids.has(x)));
      return next.size === s.size ? s : next;
    });
  }, [assets]);
  const toggle = (a: MediaAsset, range: boolean): void => {
    setSel((s) => {
      const next = new Set(s);
      if (range && anchor.current) {
        const from = assets.findIndex((x) => x.id === anchor.current);
        const to = assets.findIndex((x) => x.id === a.id);
        if (from >= 0 && to >= 0) {
          for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(assets[i]!.id);
          return next;
        }
      }
      if (next.has(a.id)) next.delete(a.id);
      else next.add(a.id);
      return next;
    });
    anchor.current = a.id;
  };
  const clearSel = useCallback(() => setSel(new Set()), []);
  useEffect(() => {
    if (!sel.size) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && !document.querySelector('.dialog-backdrop')) clearSel();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [sel.size, clearSel]);

  // dialogs -----------------------------------------------------------------------------
  const [naming, setNaming] = useState<Naming | null>(null);
  const [chooser, setChooser] = useState<Chooser | null>(null);
  const closeNaming = useCallback(() => setNaming(null), []);
  const closeChooser = useCallback(() => setChooser(null), []);
  const openDetails = (assetId: string): void => {
    const p = new URLSearchParams(params);
    p.set('asset', assetId);
    setParams(p);
  };
  const closeDetails = useCallback(() => {
    setParams((p) => {
      const n = new URLSearchParams(p);
      n.delete('asset');
      return n;
    });
  }, [setParams]);
  const reload = list.reload;

  // actions -----------------------------------------------------------------------------
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const run = async (key: string, fn: () => Promise<void>): Promise<void> => {
    if (busy) return;
    setBusy(key);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(null);
    }
  };

  const upload = (files: File[], target: string | null): void => {
    if (!files.length || !mr.upload) return;
    enqueueUploads(files, target);
  };

  const moveAssets = async (ids: string[], target: string | null): Promise<void> => {
    const todo = ids.filter(
      (x) => (assets.find((a) => a.id === x)?.folderId ?? null) !== target || !assets.some((a) => a.id === x),
    );
    if (!todo.length) return;
    const errors = await each(todo, (x) =>
      patch(`/api/v1/media/${encodeURIComponent(x)}`, { folderId: target }),
    );
    const moved = todo.length - errors.length;
    if (moved) toast(t('media.move.done', { count: moved }));
    clearSel();
    reload();
    if (errors.length) throw errors[0];
  };
  const moveFolder = async (f: MediaFolder, target: string | null): Promise<void> => {
    if ((f.parentId ?? null) === target || f.id === target) return;
    await patch(`/api/v1/media/folders/${encodeURIComponent(f.id)}`, { parentId: target });
    toast(t('media.move.doneFolder', { name: f.name }));
    reload();
  };

  const trashAssets = (ids: string[]): Promise<void> =>
    run('trash', async () => {
      const one = ids.length === 1 ? assets.find((a) => a.id === ids[0]) : undefined;
      const size = assets.filter((a) => ids.includes(a.id)).reduce((s, a) => s + a.size, 0);
      const ok = await confirm({
        title: one ? t('media.trash.titleOne') : t('media.trash.titleMany', { count: ids.length }),
        message: one
          ? t('media.trash.confirmOne', { name: one.name })
          : t('media.trash.confirmMany', { count: ids.length, size: formatBytes(size) }),
        confirmLabel: t('media.trash.action'),
        danger: true,
      });
      if (!ok) return;
      if (ids.length === 1) await del(`/api/v1/media/${encodeURIComponent(ids[0]!)}`);
      else await post('/api/v1/media/trash', { ids });
      toast(
        one ? t('media.trash.doneOne', { name: one.name }) : t('media.trash.doneMany', { count: ids.length }),
      );
      clearSel();
      reload();
    });

  const trashFolder = (f: MediaFolder): Promise<void> =>
    run(`folder:${f.id}`, async () => {
      const ok = await confirm({
        title: t('media.folder.trashTitle'),
        message: t('media.folder.trashConfirm', {
          name: f.name,
          files: f.itemCount,
          size: formatBytes(f.totalSize),
        }),
        confirmLabel: t('media.trash.action'),
        danger: true,
      });
      if (!ok) return;
      const r = await del<{ trashedAssets: number; trashedFolders: number }>(
        `/api/v1/media/folders/${encodeURIComponent(f.id)}`,
      );
      toast(
        t('media.folder.trashed', {
          name: f.name,
          files: r?.trashedAssets ?? 0,
          folders: r?.trashedFolders ?? 1,
        }),
      );
      reload();
    });

  const reprocess = (a: MediaAsset): Promise<void> =>
    run(`reprocess:${a.id}`, async () => {
      await post(`/api/v1/media/${encodeURIComponent(a.id)}/reprocess`);
      toast(t('media.reprocessStarted'));
      reload();
    });

  // drag & drop: files from the desktop upload, tiles move ------------------------------
  const [fileDrag, setFileDrag] = useState(false);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const depth = useRef(0);
  const resetDrag = useCallback(() => {
    depth.current = 0;
    setFileDrag(false);
    setDropTarget(null);
  }, []);
  useEffect(() => {
    // The highlight is reset centrally, also when the drag ends outside.
    window.addEventListener('dragend', resetDrag);
    window.addEventListener('drop', resetDrag);
    return () => {
      window.removeEventListener('dragend', resetDrag);
      window.removeEventListener('drop', resetDrag);
    };
  }, [resetDrag]);
  const pageDrag = {
    onDragEnter: (e: DragEvent) => {
      if (!hasType(e, 'Files')) return;
      depth.current++;
      setFileDrag(true);
    },
    onDragLeave: (e: DragEvent) => {
      if (!hasType(e, 'Files')) return;
      depth.current = Math.max(0, depth.current - 1);
      if (depth.current === 0) resetDrag();
    },
    onDragOver: (e: DragEvent) => {
      if (!hasType(e, 'Files')) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    },
    onDrop: (e: DragEvent) => {
      if (!hasType(e, 'Files')) return;
      e.preventDefault();
      resetDrag();
      upload(Array.from(e.dataTransfer.files), folderId);
    },
  };
  const dropOn = (target: string | null, key: string, self?: MediaFolder) => ({
    onDragOver: (e: DragEvent) => {
      const files = hasType(e, 'Files');
      if (!files && !hasType(e, MIME_ASSETS) && !hasType(e, MIME_FOLDER)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = files ? 'copy' : 'move';
      if (dropTarget !== key) setDropTarget(key);
    },
    onDragLeave: (e: DragEvent) => {
      if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
      setDropTarget((d) => (d === key ? null : d));
    },
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      e.stopPropagation();
      resetDrag();
      if (hasType(e, 'Files')) {
        upload(Array.from(e.dataTransfer.files), target);
        return;
      }
      const rawAssets = e.dataTransfer.getData(MIME_ASSETS);
      const rawFolder = e.dataTransfer.getData(MIME_FOLDER);
      if (rawAssets) {
        void run('move', () => moveAssets(JSON.parse(rawAssets) as string[], target));
      } else if (rawFolder && rawFolder !== self?.id) {
        const f = folders.find((x) => x.id === rawFolder);
        if (f) void run('move', () => moveFolder(f, target));
      }
    },
  });

  // rendering ---------------------------------------------------------------------------
  const input = useRef<HTMLInputElement>(null);
  const selected = assets.filter((a) => sel.has(a.id));
  const notFound = list.error instanceof ApiError && list.error.status === 404 && !!folderId && !term;
  const onTileClick = (a: MediaAsset, e: ReactMouseEvent): void => {
    if (e.shiftKey || e.ctrlKey || e.metaKey || sel.size > 0) toggle(a, e.shiftKey);
    else openDetails(a.id);
  };
  const subtitle = list.data
    ? term
      ? t('media.searchSummary', { folders: folders.length, files: list.data.total })
      : t('media.summary', { folders: folders.length, files: list.data.total })
    : undefined;

  return (
    <div className="page mm-page" {...pageDrag}>
      <PageHeader
        title={t('media.title')}
        subtitle={subtitle}
        actions={
          mr.upload && (
            <>
              <Button
                icon={<FolderPlus size={16} />}
                onClick={() => setNaming({ type: 'create' })}
                disabled={notFound}
              >
                {t('media.folder.new')}
              </Button>
              <input
                ref={input}
                type="file"
                multiple
                hidden
                accept={ACCEPT.all}
                onChange={(e) => {
                  upload(Array.from(e.target.files ?? []), folderId);
                  e.target.value = '';
                }}
              />
              <Button
                variant="primary"
                icon={<Upload size={16} />}
                onClick={() => input.current?.click()}
                disabled={notFound}
              >
                {t('media.upload.button')}
              </Button>
            </>
          )
        }
      />
      <div className="mm-toolbar">
        {term ? (
          <div className="mm-note">{t('media.searchResults', { q: term })}</div>
        ) : (
          <Breadcrumb
            path={crumbs}
            onOpen={(fid) => navigate(folderRoute(fid))}
            crumbProps={(fid) => ({
              ...dropOn(fid, `crumb:${fid ?? ROOT}`),
              className: dropTarget === `crumb:${fid ?? ROOT}` ? 'drop' : '',
            })}
          />
        )}
        <div className="mm-tools">
          <div className="segmented" role="group" aria-label={t('media.kind.label')}>
            {(['', 'image', 'video', 'audio'] as const).map((k) => (
              <button
                key={k || 'all'}
                type="button"
                className={kind === k ? 'active' : ''}
                aria-pressed={kind === k}
                onClick={() => setKind(k)}
              >
                {t(`media.kind.plural.${k || 'all'}`)}
              </button>
            ))}
          </div>
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
        </div>
      </div>

      {sel.size > 0 && (
        <div className="mm-selbar" role="region" aria-label={t('media.selection')}>
          <span className="mm-selbar-count">
            {t('media.selected', {
              count: sel.size,
              size: formatBytes(selected.reduce((s, a) => s + a.size, 0)),
            })}
          </span>
          <Button
            className="btn-sm"
            onClick={() => setSel(new Set(assets.map((a) => a.id)))}
            disabled={sel.size === assets.length}
          >
            {t('media.selectAll')}
          </Button>
          {selected.every(mr.ownAsset) && (
            <Button
              className="btn-sm"
              icon={<FolderInput size={14} />}
              busy={busy === 'move'}
              onClick={() => setChooser({ type: 'assets', ids: [...sel], source: uniformFolder(selected) })}
            >
              {t('media.move.button')}
            </Button>
          )}
          {mr.manage && (
            <Button
              className="btn-sm"
              variant="danger"
              icon={<Trash2 size={14} />}
              busy={busy === 'trash'}
              onClick={() => void trashAssets([...sel])}
            >
              {t('media.trash.action')}
            </Button>
          )}
          <Button className="btn-sm" variant="ghost" icon={<X size={14} />} onClick={clearSel}>
            {t('media.clearSelection')}
          </Button>
        </div>
      )}
      {!!error && (
        <div className="form-error" role="alert" style={{ marginBottom: 12 }}>
          {mediaErrorText(error)}
        </div>
      )}

      {notFound ? (
        <div className="state state-empty">
          <div>{t('media.folder.gone')}</div>
          <Button onClick={() => navigate('/media')}>{t('media.backToRoot')}</Button>
        </div>
      ) : (
        <DataState
          loading={list.loading && !list.data}
          error={list.error}
          onRetry={list.reload}
          empty={!!list.data && folders.length + assets.length === 0}
          emptyText={term ? t('media.noResults') : folderId ? t('media.emptyFolder') : t('media.emptyRoot')}
        >
          {folders.length > 0 && (
            <>
              <h2 className="mm-section">{t('media.folders')}</h2>
              <div className="mm-grid mm-grid-folders">
                {folders.map((f) => (
                  <FolderTile
                    key={f.id}
                    folder={f}
                    className={dropTarget === `folder:${f.id}` ? 'drop' : ''}
                    onOpen={() => navigate(folderRoute(f.id))}
                    tileProps={{
                      draggable: true,
                      onDragStart: (e) => {
                        e.dataTransfer.setData(MIME_FOLDER, f.id);
                        e.dataTransfer.effectAllowed = 'move';
                      },
                      ...dropOn(f.id, `folder:${f.id}`, f),
                    }}
                    actions={
                      <>
                        {mr.ownFolder(f) && (
                          <IconButton
                            label={t('media.folder.rename')}
                            onClick={() => setNaming({ type: 'rename', folder: f })}
                          >
                            <Pencil size={15} />
                          </IconButton>
                        )}
                        {mr.ownFolder(f) && (
                          <IconButton
                            label={t('media.move.button')}
                            onClick={() => setChooser({ type: 'folder', folder: f })}
                          >
                            <FolderInput size={15} />
                          </IconButton>
                        )}
                        {mr.manage && (
                          <IconButton
                            label={t('media.trash.action')}
                            disabled={busy === `folder:${f.id}`}
                            onClick={() => void trashFolder(f)}
                          >
                            <Trash2 size={15} />
                          </IconButton>
                        )}
                      </>
                    }
                  />
                ))}
              </div>
            </>
          )}
          {assets.length > 0 && (
            <>
              <h2 className="mm-section">{t('media.files')}</h2>
              <div className="mm-grid">
                {assets.map((a) => (
                  <AssetTile
                    key={a.id}
                    asset={a}
                    selected={sel.has(a.id)}
                    check={{
                      checked: sel.has(a.id),
                      onToggle: (e) => {
                        e.stopPropagation();
                        toggle(a, e.shiftKey);
                      },
                    }}
                    onOpen={(e) => onTileClick(a, e)}
                    status={
                      <AssetStatus
                        asset={a}
                        busy={busy === `reprocess:${a.id}`}
                        onReprocess={() => void reprocess(a)}
                      />
                    }
                    tileProps={{
                      draggable: true,
                      onDragStart: (e) => {
                        const ids = sel.has(a.id) ? [...sel] : [a.id];
                        e.dataTransfer.setData(MIME_ASSETS, JSON.stringify(ids));
                        e.dataTransfer.effectAllowed = 'move';
                      },
                    }}
                    actions={
                      <>
                        <IconButton
                          label={t('media.move.button')}
                          onClick={() => setChooser({ type: 'assets', ids: [a.id], source: a.folderId })}
                        >
                          <FolderInput size={15} />
                        </IconButton>
                        <IconButton
                          label={t('media.trash.action')}
                          disabled={busy === 'trash'}
                          onClick={() => void trashAssets([a.id])}
                        >
                          <Trash2 size={15} />
                        </IconButton>
                      </>
                    }
                  />
                ))}
              </div>
            </>
          )}
          {!!list.data && list.data.total > assets.length && (
            <div className="mm-more">
              <Button
                busy={list.loading}
                disabled={limit >= 5000}
                onClick={() => setLimit((l) => Math.min(5000, l + PAGE))}
              >
                {t('media.showMore', { shown: assets.length, total: list.data.total })}
              </Button>
            </div>
          )}
        </DataState>
      )}

      {fileDrag && (
        <div className="mm-dropzone" aria-hidden>
          <div>
            <Upload size={20} />
            {t('media.upload.dropHere', { name: here })}
          </div>
        </div>
      )}
      <UploadPanel />

      {naming?.type === 'create' && (
        <NameDialog
          title={t('media.folder.new')}
          label={t('media.folder.name')}
          confirmLabel={t('media.folder.create')}
          onClose={closeNaming}
          onSubmit={async (name) => {
            await post('/api/v1/media/folders', { name, parentId: folderId });
            toast(t('media.folder.created', { name }));
            setNaming(null);
            reload();
          }}
        />
      )}
      {naming?.type === 'rename' && (
        <NameDialog
          title={t('media.folder.rename')}
          label={t('media.folder.name')}
          initial={naming.folder.name}
          confirmLabel={t('common.action.save')}
          onClose={closeNaming}
          onSubmit={async (name) => {
            await patch(`/api/v1/media/folders/${encodeURIComponent(naming.folder.id)}`, { name });
            toast(t('media.renamed'));
            setNaming(null);
            reload();
          }}
        />
      )}
      {chooser?.type === 'assets' && (
        <FolderChooserDialog
          title={t('media.move.titleMany', { count: chooser.ids.length })}
          start={folder}
          source={chooser.source}
          onClose={closeChooser}
          onConfirm={async (target) => {
            await moveAssets(chooser.ids, target);
            setChooser(null);
          }}
        />
      )}
      {chooser?.type === 'folder' && (
        <FolderChooserDialog
          title={t('media.move.titleFolder', { name: chooser.folder.name })}
          start={chooser.folder.parentId ?? ROOT}
          source={chooser.folder.parentId}
          blocked={[chooser.folder.id]}
          onClose={closeChooser}
          onConfirm={async (target) => {
            await moveFolder(chooser.folder, target);
            setChooser(null);
          }}
        />
      )}
      {openAsset && <AssetDialog id={openAsset} onClose={closeDetails} onChanged={reload} />}
    </div>
  );
}

/** The common folder of the assets, or undefined when they are spread over several. */
function uniformFolder(list: MediaAsset[]): string | null | undefined {
  if (!list.length) return undefined;
  const first = list[0]!.folderId ?? null;
  return list.every((a) => (a.folderId ?? null) === first) ? first : undefined;
}
