// The presentation editor page (/presentations/:id).
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useBlocker, useNavigate, useParams } from 'react-router';
import { ArrowLeft, Eye, History, Play, RotateCcw, Upload } from 'lucide-react';
import type { Presentation } from '@opensignage/contracts';
import { ApiError, api, errorText, get, post } from '../../../lib/api';
import { useQuery, useT } from '../../../lib/hooks';
import { useConfirm, useToast } from '../../../components/feedback';
import { Badge, Button, DataState, Dialog } from '../../../components/ui';
import { EditorStore } from './store';
import { useEditor } from './useEditor';
import { Canvas } from './Canvas';
import { Toolbar } from './Toolbar';
import { Panel } from './Panel';
import { SlideRail } from './SlideRail';
import { Preview } from './Preview';
import { useEditorLive, useFonts, useRenderEnv } from './env';
import { contentDefaults, type PresentationDefaults } from './factory';
import { textSession } from './TextEditor';
import { HistoryDialog } from './History';
import { CopyForZoneButton } from '../../../components/CopyForZone';
import type { ContentAccess } from '../../../lib/rights';
import './editor.css';

interface Detail {
  id: string;
  name: string;
  draft: Presentation;
  draftRevision: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  /** Rights of the user (GET only; missing on older servers = full access). */
  access?: ContentAccess;
  /** Zones the user may give an own copy of this presentation. */
  copyZones?: string[];
}

type SaveState = 'saved' | 'saving' | 'unsaved' | 'error';

const AUTOSAVE_MS = 1500;

export function EditorPage() {
  const { id = '' } = useParams();
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState<unknown>(null);
  const load = useCallback(() => {
    setError(null);
    get<Detail>(`/api/v1/presentations/${id}`).then(setDetail, setError);
  }, [id]);
  useEffect(load, [load]);
  return (
    <DataState loading={!detail && !error} error={error} onRetry={load}>
      {detail && <EditorInner key={detail.id} detail={detail} reload={load} />}
    </DataState>
  );
}

function EditorInner({ detail, reload }: { detail: Detail; reload: () => void }) {
  const t = useT();
  const toast = useToast();
  const confirm = useConfirm();
  const navigate = useNavigate();
  // Content that also runs in zones the user may not change opens read only.
  const readOnly = detail.access ? !detail.access.edit : false;
  const store = useMemo(() => {
    const s = new EditorStore(detail.draft, detail.id);
    s.readOnly = readOnly;
    return s;
  }, [detail, readOnly]);
  const snap = useEditor(store);
  const live = useEditorLive();
  const fonts = useFonts();
  const env = useRenderEnv(snap.doc, live);
  // Start values of new slides and texts (Settings → Defaults).
  const settings = useQuery<{ defaults: { slideMs: number }; presentations: PresentationDefaults }>('/api/v1/settings');
  const defaults = useMemo(() => contentDefaults(settings.data ?? undefined), [settings.data]);
  const [save, setSave] = useState<SaveState>('saved');
  const [saveError, setSaveError] = useState<string>('');
  const [meta, setMeta] = useState({ revision: detail.draftRevision, published: detail.publishedVersion, hasChanges: detail.hasChanges });
  const [conflict, setConflict] = useState(false);
  const [preview, setPreview] = useState(false);
  const [history, setHistory] = useState(false);
  const [busy, setBusy] = useState(false);
  const saving = useRef<Promise<void> | null>(null);
  const metaRef = useRef(meta);
  metaRef.current = meta;

  const flush = useCallback(async (): Promise<boolean> => {
    if (saving.current) await saving.current;
    if (!store.dirty) return true;
    const doc = store.presentation;
    setSave('saving');
    let ok = false;
    saving.current = (async () => {
      try {
        // keepalive: a save that is running when the tab closes still reaches the server.
        const res = await api<{ revision: number; hasChanges: boolean }>(`/api/v1/presentations/${detail.id}/draft`, {
          method: 'PUT',
          body: { revision: metaRef.current.revision, doc },
          keepalive: true,
        });
        setMeta((m) => ({ ...m, revision: res.revision, hasChanges: res.hasChanges }));
        store.markSaved(doc);
        setSave(store.dirty ? 'unsaved' : 'saved');
        setSaveError('');
        ok = true;
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) setConflict(true);
        setSave('error');
        setSaveError(errorText(e));
      } finally {
        saving.current = null;
      }
    })();
    await saving.current;
    return ok;
  }, [detail.id, store]);

  // Autosave (debounced). Saving never publishes.
  useEffect(() => {
    if (!snap.dirty) return;
    setSave((s) => (s === 'error' ? s : 'unsaved'));
    const timer = window.setTimeout(() => void flush(), AUTOSAVE_MS);
    return () => clearTimeout(timer);
  }, [snap.version, snap.dirty, flush]);

  // Media limits refused by the store (insert, paste, duplicate, copy to all slides).
  useEffect(() => {
    store.onLimit = (kind) => toast(kind === 'video' ? t('editor.limit.video') : t('editor.limit.audio'), 'error');
    return () => {
      store.onLimit = null;
    };
  }, [store, toast, t]);

  // Closing or reloading the tab, or hiding it: save right away instead of asking with the
  // browser's "leave page?" prompt. keepalive lets the request finish after unload.
  useEffect(() => {
    const onHide = (): void => {
      if (store.dirty && !saving.current) void flush();
    };
    const onVisibility = (): void => {
      if (document.visibilityState === 'hidden') onHide();
    };
    window.addEventListener('pagehide', onHide);
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      window.removeEventListener('pagehide', onHide);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [store, flush]);

  // Leaving the editor inside the app: save first; ask (own dialog) only if saving failed.
  const blocker = useBlocker(({ currentLocation, nextLocation }) => currentLocation.pathname !== nextLocation.pathname && (store.dirty || saving.current !== null));
  const leaving = useRef(false);
  useEffect(() => {
    if (blocker.state !== 'blocked' || leaving.current) return;
    leaving.current = true;
    void (async () => {
      try {
        if (await flush()) {
          blocker.proceed();
          return;
        }
        const leave = await confirm({ title: t('editor.leave.title'), message: t('editor.leave.text'), confirmLabel: t('editor.leave.confirm'), danger: true });
        if (leave) blocker.proceed();
        else blocker.reset();
      } finally {
        leaving.current = false;
      }
    })();
  }, [blocker, flush, confirm, t]);

  // Keyboard shortcuts (not while typing in inputs or editing text).
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      const target = e.target as HTMLElement;
      if (textSession.view || target.closest('input, textarea, select, [contenteditable=true], [role=menu], [role=dialog]')) return;
      const mod = e.ctrlKey || e.metaKey;
      const k = e.key.toLowerCase();
      if (mod && k === 'z' && !e.shiftKey) store.undo();
      else if (mod && (k === 'y' || (k === 'z' && e.shiftKey))) store.redo();
      else if (mod && k === 'c') store.copy();
      else if (mod && k === 'x') store.cut();
      else if (mod && k === 'v') store.paste();
      else if (mod && k === 'd') store.duplicate();
      else if (mod && k === 'a') store.selectAll();
      else if (mod && k === 'g' && e.shiftKey) store.ungroup();
      else if (mod && k === 'g') store.group();
      else if (k === 'delete' || k === 'backspace') store.deleteSelection();
      else if (k === 'escape') store.escape();
      else if (k.startsWith('arrow') && store.selected.length) {
        const d = e.shiftKey ? 10 : 1;
        const dx = k === 'arrowleft' ? -d : k === 'arrowright' ? d : 0;
        const dy = k === 'arrowup' ? -d : k === 'arrowdown' ? d : 0;
        store.updateElements(store.selected.filter((x) => !x.locked).map((x) => x.id), (x) => {
          x.x += dx;
          x.y += dy;
        });
      } else return;
      e.preventDefault();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [store]);

  const publish = async (): Promise<void> => {
    const ok = await confirm({ title: t('editor.publish'), message: t('editor.publishConfirm'), confirmLabel: t('editor.publish') });
    if (!ok) return;
    setBusy(true);
    try {
      if (!(await flush())) return;
      const r = await post<{ publishedVersion: number }>(`/api/v1/presentations/${detail.id}/publish`);
      setMeta((m) => ({ ...m, published: r.publishedVersion, hasChanges: false }));
      toast(t('editor.published'));
    } catch (e) {
      toast(errorText(e), 'error');
    } finally {
      setBusy(false);
    }
  };

  const discard = async (): Promise<void> => {
    const ok = await confirm({ title: t('editor.discard'), message: t('editor.discardConfirm'), confirmLabel: t('editor.discard'), danger: true });
    if (!ok) return;
    setBusy(true);
    try {
      const d = await post<Detail>(`/api/v1/presentations/${detail.id}/discard`);
      store.reset(d.draft);
      setMeta({ revision: d.draftRevision, published: d.publishedVersion, hasChanges: d.hasChanges });
      setSave('saved');
      toast(t('editor.discarded'));
    } catch (e) {
      toast(errorText(e), 'error');
    } finally {
      setBusy(false);
    }
  };

  // Unsaved changes are saved by the navigation blocker above.
  const back = (): void => {
    void navigate('/presentations');
  };

  // Nothing to publish or discard: the saved draft equals the published version.
  const unchanged = !meta.hasChanges && save === 'saved';
  const statusBadge =
    save === 'saving' ? <Badge>{t('common.state.saving')}</Badge> : save === 'unsaved' ? <Badge tone="warning">{t('editor.unsaved')}</Badge> : save === 'error' ? <Badge tone="danger">{t('editor.saveFailed')}</Badge> : <Badge tone="success">{t('editor.saved')}</Badge>;

  return (
    <div
      className={`editor ${readOnly ? 'read-only' : ''}`}
      onContextMenu={(e) => {
        // Never the browser menu in the editor; text fields keep theirs (spelling, clipboard).
        if (!(e.target as HTMLElement).closest('input, textarea, [contenteditable=true]')) e.preventDefault();
      }}
    >
      <div className="editor-head">
        <button type="button" className="icon-btn" onClick={back} title={t('common.action.back')}>
          <ArrowLeft size={18} />
        </button>
        {/* The name is changed in the panel (Presentation tab), not in the header. */}
        <h1 className="editor-title" title={snap.doc.name}>
          {snap.doc.name}
        </h1>
        <span className="faint">
          {snap.doc.width}×{snap.doc.height}
        </span>
        {readOnly ? (
          <Badge>
            <Eye size={12} /> {t('admin.readOnly.badge')}
          </Badge>
        ) : (
          statusBadge
        )}
        {meta.published ? meta.hasChanges ? <Badge tone="warning">{t('editor.changesNotPublished')}</Badge> : <Badge tone="primary">{t('editor.publishedVersion', { v: meta.published })}</Badge> : <Badge>{t('editor.neverPublished')}</Badge>}
        <div className="spacer" />
        <Button icon={<History size={16} />} onClick={() => setHistory(true)}>
          {t('editor.history.title')}
        </Button>
        <Button icon={<Play size={16} />} onClick={() => setPreview(true)}>
          {t('editor.preview')}
        </Button>
        {readOnly ? (
          <CopyForZoneButton kind="presentations" id={detail.id} zoneIds={detail.copyZones ?? []} onCopied={(newId) => void navigate(`/presentations/${newId}`)} />
        ) : (
          <>
            <Button icon={<RotateCcw size={16} />} disabled={!meta.published || unchanged || busy} onClick={() => void discard()}>
              {t('editor.discard')}
            </Button>
            <Button variant="primary" icon={<Upload size={16} />} busy={busy} disabled={!!meta.published && unchanged} onClick={() => void publish()}>
              {t('editor.publish')}
            </Button>
          </>
        )}
      </div>
      {save === 'error' && saveError && <div className="banner banner-warning">{saveError}</div>}
      {readOnly && <div className="banner banner-info">{t(detail.copyZones?.length ? 'admin.readOnly.sharedCopy' : 'admin.readOnly.shared')}</div>}
      {!readOnly && <Toolbar store={store} defaults={defaults} />}
      <div className="editor-body">
        <SlideRail store={store} env={{ ...env, mode: 'thumb' }} defaults={defaults} />
        <Canvas store={store} env={env} />
        <fieldset className="editor-lock" disabled={readOnly}>
          <Panel store={store} fonts={fonts} />
        </fieldset>
      </div>
      {history && (
        <HistoryDialog
          id={detail.id}
          readOnly={readOnly}
          onClose={() => setHistory(false)}
          onRestored={(d) => {
            store.reset(d.draft);
            setMeta({ revision: d.draftRevision, published: d.publishedVersion, hasChanges: d.hasChanges });
            setSave('saved');
            setHistory(false);
            toast(d.warnings?.length ? t('editor.history.restoredWarnings', { count: d.warnings.length }) : t('editor.history.restored'), d.warnings?.length ? 'error' : 'success');
          }}
        />
      )}
      {preview && <Preview doc={snap.doc} live={live} startSlideId={snap.slideId} onClose={() => setPreview(false)} />}
      {conflict && (
        <Dialog
          title={t('editor.conflictTitle')}
          onClose={() => setConflict(false)}
          footer={
            <>
              <Button onClick={() => (setConflict(false), reload())}>{t('editor.conflictReload')}</Button>
              <Button
                variant="danger"
                onClick={async () => {
                  const d = await get<Detail>(`/api/v1/presentations/${detail.id}`);
                  setMeta((m) => ({ ...m, revision: d.draftRevision }));
                  setConflict(false);
                  void flush();
                }}
              >
                {t('editor.conflictOverwrite')}
              </Button>
            </>
          }
        >
          <p className="muted">{t('editor.conflictText')}</p>
        </Dialog>
      )}
    </div>
  );
}
