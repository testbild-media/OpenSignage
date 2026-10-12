import { useCallback, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { ChevronRight, Copy, Film, GripVertical, ListVideo, Pencil, Plus, Presentation as PresIcon, Trash2, Upload, Image as ImageIcon } from 'lucide-react';
import { MAX_PLAYLIST_SCREENS, type Playlist, type PlaylistCell, type PlaylistItem, type Presentation } from '@opensignage/contracts';
import type { Feature, NavChild } from '../types';
import { ApiError, del, errorText, get, patch, post, put } from '../../lib/api';
import { useAction, useQuery, useT } from '../../lib/hooks';
import { useEvents } from '../../lib/events';
import { useConfirm, useToast } from '../../components/feedback';
import { Badge, Button, DataState, Dialog, ErrorText, IconButton, PageHeader, Select, TextInput } from '../../components/ui';
import { newId } from '../presentations/editor/store';
import { MediaBrowser } from '../../components/MediaPicker';
import { isUsable } from '../media/model';
import { allowedModes, clampCount, clampMs, defaultPlay, fmtDuration, type ItemPass, normalizeItem, repeatHints, switchMode, toStep } from './itemRules';
import './playlists.css';

interface Summary {
  id: string;
  name: string;
  itemCount: number;
  publishedVersion: number | null;
  hasChanges: boolean;
}
interface Detail {
  id: string;
  name: string;
  draft: Playlist;
  draftRevision: number;
  publishedVersion: number | null;
  hasChanges: boolean;
  runtimeMs: number;
  runtimeExact: boolean;
  /** Length of one pass per item id (server estimate from the published content). */
  itemPasses?: Record<string, ItemPass>;
}
interface Defaults {
  defaults: { mode: 'duration' | 'repeat'; durationMs: number; repeat: number; fit: 'contain' | 'cover' | 'fill' };
}
interface MediaAsset {
  id: string;
  name: string;
  kind: 'image' | 'video' | 'audio';
  meta?: { durationMs?: number };
}
interface PresSummary {
  id: string;
  name: string;
  publishedVersion: number | null;
}

function PlaylistsPage() {
  const t = useT();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const toast = useToast();
  const list = useQuery<{ playlists: Summary[] }>('/api/v1/playlists');
  useEvents(['content'], () => list.reload());
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState('');
  const create = useAction(async () => {
    const r = await post<{ id: string }>('/api/v1/playlists', { name: name.trim() });
    navigate(`/playlists/${r.id}`);
  });
  const duplicate = useAction(async (p: Summary) => {
    await post(`/api/v1/playlists/${p.id}/duplicate`);
    list.reload();
  });
  const remove = useAction(async (p: Summary) => {
    if (!(await confirm({ title: t('playlists.delete'), message: t('playlists.deleteConfirm', { name: p.name }), danger: true, confirmLabel: t('common.action.delete') }))) return;
    await del(`/api/v1/playlists/${p.id}`);
    toast(t('playlists.deleted'));
    list.reload();
  });
  return (
    <div className="page">
      <PageHeader
        title={t('admin.nav.playlists')}
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setCreating(true)}>
            {t('playlists.new')}
          </Button>
        }
      />
      <ErrorText error={remove.error ?? duplicate.error} />
      <DataState loading={list.loading && !list.data} error={list.error} onRetry={list.reload} empty={list.data?.playlists.length === 0} emptyText={t('playlists.empty')}>
        <table className="table">
          <thead>
            <tr>
              <th>{t('fields.name')}</th>
              <th>{t('playlists.items')}</th>
              <th>{t('playlists.state')}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list.data?.playlists.map((p) => (
              <tr key={p.id} className="clickable" onClick={() => navigate(`/playlists/${p.id}`)}>
                <td>{p.name}</td>
                <td>{p.itemCount}</td>
                <td>{p.publishedVersion ? p.hasChanges ? <Badge tone="warning">{t('editor.changesNotPublished')}</Badge> : <Badge tone="success">{t('pres.published')}</Badge> : <Badge>{t('editor.neverPublished')}</Badge>}</td>
                <td className="row-actions" onClick={(e) => e.stopPropagation()}>
                  <IconButton label={t('common.action.edit')} onClick={() => navigate(`/playlists/${p.id}`)}>
                    <Pencil size={16} />
                  </IconButton>
                  <IconButton label={t('editor.duplicate')} onClick={() => void duplicate.run(p)}>
                    <Copy size={16} />
                  </IconButton>
                  <IconButton label={t('common.action.delete')} onClick={() => void remove.run(p)}>
                    <Trash2 size={16} />
                  </IconButton>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </DataState>
      {creating && (
        <Dialog
          title={t('playlists.new')}
          onClose={() => setCreating(false)}
          footer={
            <>
              <Button onClick={() => setCreating(false)}>{t('common.action.cancel')}</Button>
              <Button variant="primary" busy={create.busy} disabled={!name.trim()} onClick={() => void create.run()}>
                {t('pres.create')}
              </Button>
            </>
          }
        >
          <div className="form">
            <TextInput label={t('fields.name')} value={name} autoFocus onChange={(e) => setName(e.target.value)} />
            <ErrorText error={create.error} />
          </div>
        </Dialog>
      )}
    </div>
  );
}

type DragPayload = { kind: 'presentation' | 'slide' | 'media'; refId: string; presentationId?: string; mediaKind?: string } | { move: string };

function Sidebar({ onAdd }: { onAdd: (p: Exclude<DragPayload, { move: string }>) => void }) {
  const t = useT();
  const [tab, setTab] = useState<'media' | 'presentations'>('presentations');
  const [folder, setFolder] = useState('root');
  const [q, setQ] = useState('');
  const term = q.trim();
  const pres = useQuery<{ presentations: PresSummary[] }>(tab === 'presentations' && !term ? '/api/v1/presentations' : null);
  const search = useQuery<{ presentations: { id: string; name: string }[]; slides: { slideId: string; name: string; presentationId: string; presentationName: string }[] }>(term ? `/api/v1/content/search?q=${encodeURIComponent(term)}` : null);
  const contentHits = (search.data?.presentations.length ?? 0) + (search.data?.slides.length ?? 0);
  const [open, setOpen] = useState<string | null>(null);
  const slides = useQuery<{ draft: Presentation }>(open ? `/api/v1/presentations/${open}` : null);
  const drag = (p: DragPayload) => (e: React.DragEvent) => {
    e.dataTransfer.setData('application/x-os-item', JSON.stringify(p));
    e.dataTransfer.effectAllowed = 'copyMove';
  };
  // Opening a folder from the media results leaves the search and shows that folder.
  const openFolder = (f: string): void => {
    setFolder(f);
    if (term) {
      setQ('');
      setTab('media');
    }
  };
  const media = (query?: string) => (
    <MediaBrowser
      layout="list"
      folder={folder}
      onFolderChange={openFolder}
      kinds={['image', 'video']}
      search={false}
      query={query}
      kindFilter={false}
      canPick={isUsable}
      onPick={(a) => onAdd({ kind: 'media', refId: a.id, mediaKind: a.kind })}
      assetProps={(a) => ({ draggable: true, onDragStart: drag({ kind: 'media', refId: a.id, mediaKind: a.kind }) })}
    />
  );
  return (
    <aside className="pl-sidebar">
      <input
        type="search"
        className="input input-sm"
        placeholder={t('playlists.searchAll')}
        aria-label={t('playlists.searchAll')}
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {!term && (
        <div className="tabs">
          <button className={tab === 'presentations' ? 'active' : ''} onClick={() => setTab('presentations')}>
            {t('admin.nav.presentations')}
          </button>
          <button className={tab === 'media' ? 'active' : ''} onClick={() => setTab('media')}>
            {t('playlists.media')}
          </button>
        </div>
      )}
      <div className="pl-source">
        {term && (
          <DataState loading={search.loading && !search.data} error={search.error}>
            <div className="pl-group">{t('playlists.searchContent')}</div>
            {search.data && contentHits === 0 && <div className="faint pad">{t('playlists.noContentMatch')}</div>}
            {search.data?.presentations.map((p) => (
              <div key={p.id} className="pl-src" draggable onDragStart={drag({ kind: 'presentation', refId: p.id })} onDoubleClick={() => onAdd({ kind: 'presentation', refId: p.id })}>
                <PresIcon size={14} /> {p.name}
              </div>
            ))}
            {search.data?.slides.map((s) => (
              <div key={s.slideId} className="pl-src" draggable onDragStart={drag({ kind: 'slide', refId: s.slideId, presentationId: s.presentationId })} onDoubleClick={() => onAdd({ kind: 'slide', refId: s.slideId, presentationId: s.presentationId })}>
                <ChevronRight size={14} /> {s.name || s.slideId.slice(0, 6)} <span className="faint">· {s.presentationName}</span>
              </div>
            ))}
          </DataState>
        )}
        {term && (
          <>
            <div className="pl-group">{t('playlists.media')}</div>
            {media(term)}
          </>
        )}
        {!term && tab === 'presentations' && (
          <DataState loading={pres.loading && !pres.data} error={pres.error}>
            {pres.data?.presentations.map((p) => (
              <div key={p.id}>
                <div className={`pl-src ${p.publishedVersion ? '' : 'faint'}`} draggable={!!p.publishedVersion} onDragStart={drag({ kind: 'presentation', refId: p.id })} onDoubleClick={() => p.publishedVersion && onAdd({ kind: 'presentation', refId: p.id })}>
                  <button type="button" className="icon-btn" onClick={() => setOpen(open === p.id ? null : p.id)}>
                    <ChevronRight size={14} className={open === p.id ? 'rot90' : ''} />
                  </button>
                  <PresIcon size={14} /> {p.name}
                  {!p.publishedVersion && <span className="faint"> ({t('editor.neverPublished')})</span>}
                </div>
                {open === p.id &&
                  slides.data?.draft.slides.map((s, i) => (
                    <div key={s.id} className="pl-src pl-slide" draggable onDragStart={drag({ kind: 'slide', refId: s.id, presentationId: p.id })} onDoubleClick={() => onAdd({ kind: 'slide', refId: s.id, presentationId: p.id })}>
                      {s.name || t('editor.slide.number', { n: i + 1 })}
                    </div>
                  ))}
              </div>
            ))}
          </DataState>
        )}
        {!term && tab === 'media' && media()}
      </div>
      <div className="faint pad">{t('playlists.dragHint')}</div>
    </aside>
  );
}

function ItemDialog({
  item,
  mediaKind,
  pass,
  defaultMs,
  tracks,
  onSave,
  onClose,
}: {
  item: PlaylistItem;
  mediaKind?: string;
  pass?: ItemPass;
  defaultMs: number;
  tracks: number;
  onSave: (i: PlaylistItem) => void;
  onClose: () => void;
}) {
  const t = useT();
  // An entry that breaks the rules (a single slide with a duration from an older playlist)
  // is converted when it is opened; saving stores the converted entry.
  const [initial] = useState(() => normalizeItem(item, mediaKind, defaultMs, tracks));
  const [it, setIt] = useState(initial.item);
  const modes = allowedModes(it.kind, mediaKind, tracks);
  return (
    <Dialog
      title={t('playlists.editItem')}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{t('common.action.cancel')}</Button>
          <Button variant="primary" onClick={() => onSave(it)}>
            {t('common.action.save')}
          </Button>
        </>
      }
    >
      <div className="form">
        {initial.converted && it.kind === 'slide' && tracks === 1 && <div className="pl-note">{t('playlists.slideConverted')}</div>}
        {modes.length > 1 && (
          <Select
            label={t('playlists.playMode')}
            value={it.play.mode}
            onChange={(e) => setIt({ ...it, play: switchMode(e.target.value === 'repeat' ? 'repeat' : 'duration', defaultMs) })}
            options={[
              { value: 'duration', label: t('admin.defaults.modeDuration') },
              { value: 'repeat', label: t('admin.defaults.modeRepeat') },
            ]}
          />
        )}
        {it.play.mode === 'duration' ? (
          <TextInput
            label={t('admin.defaults.durationSec')}
            type="number"
            min={1}
            value={Math.round(it.play.ms / 1000)}
            hint={tracks > 1 ? t('playlists.tracks.durationHint') : modes.length > 1 ? t('playlists.durationHint') : undefined}
            onChange={(e) => setIt({ ...it, play: { mode: 'duration', ms: clampMs(Number(e.target.value) * 1000) } })}
          />
        ) : (
          <TextInput
            label={t('admin.defaults.repeat')}
            type="number"
            min={1}
            max={1000}
            value={it.play.count}
            hint={repeatHints(it.kind, pass, it.play.count)
              .map((h) => t(h.key, h.params))
              .join(' ')}
            onChange={(e) => setIt({ ...it, play: { mode: 'repeat', count: clampCount(Number(e.target.value)) } })}
          />
        )}
        {it.kind === 'media' && (
          <Select label={t('admin.defaults.fit')} value={it.fit ?? 'contain'} onChange={(e) => setIt({ ...it, fit: e.target.value as 'contain' })} options={['contain', 'cover', 'fill'].map((f) => ({ value: f, label: t(`admin.fit.${f}`) }))} />
        )}
      </div>
    </Dialog>
  );
}

function PlaylistEditor() {
  const t = useT();
  const { id = '' } = useParams();
  const toast = useToast();
  const confirm = useConfirm();
  const [detail, setDetail] = useState<Detail | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [doc, setDoc] = useState<Playlist | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saveError, setSaveError] = useState<string>('');
  const [editing, setEditing] = useState<PlaylistItem | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [names, setNames] = useState<Record<string, { name: string; kind?: string }>>({});
  const settings = useQuery<Defaults>('/api/v1/settings');
  const revision = useRef(0);
  const load = useCallback(() => {
    get<Detail>(`/api/v1/playlists/${id}`).then(
      (d) => {
        setDetail(d);
        setDoc(d.draft);
        revision.current = d.draftRevision;
        setDirty(false);
      },
      setLoadError,
    );
  }, [id]);
  useEffect(load, [load]);

  // Resolve display names for items.
  useEffect(() => {
    if (!doc) return;
    const refs: (PlaylistItem | PlaylistCell)[] = doc.items.flatMap((i) => [i, ...(i.cells ?? []).filter((c): c is PlaylistCell => !!c)]);
    const missing = refs.filter((i, n) => !names[i.refId] && refs.findIndex((x) => x.refId === i.refId) === n);
    if (!missing.length) return;
    void (async () => {
      const next: Record<string, { name: string; kind?: string }> = {};
      for (const i of missing) {
        try {
          if (i.kind === 'media') {
            const a = await get<MediaAsset>(`/api/v1/media/${i.refId}`);
            next[i.refId] = { name: a.name, kind: a.kind };
          } else {
            const p = await get<{ draft: Presentation }>(`/api/v1/presentations/${i.kind === 'slide' ? i.presentationId : i.refId}`);
            if (i.kind === 'slide') {
              const idx = p.draft.slides.findIndex((s) => s.id === i.refId);
              next[i.refId] = { name: `${p.draft.name} › ${p.draft.slides[idx]?.name || t('editor.slide.number', { n: idx + 1 })}` };
            } else next[i.refId] = { name: p.draft.name };
          }
        } catch {
          next[i.refId] = { name: t('playlists.missing') };
        }
      }
      setNames((n) => ({ ...n, ...next }));
    })();
  }, [doc, names, t]);

  const save = useCallback(async (): Promise<boolean> => {
    if (!doc) return false;
    try {
      const res = await put<{ revision: number; hasChanges: boolean; runtimeMs: number; runtimeExact: boolean; itemPasses?: Record<string, ItemPass> }>(`/api/v1/playlists/${id}/draft`, {
        revision: revision.current,
        doc,
      });
      revision.current = res.revision;
      setDetail((prev) =>
        prev ? { ...prev, draftRevision: res.revision, hasChanges: res.hasChanges, runtimeMs: res.runtimeMs, runtimeExact: res.runtimeExact, itemPasses: res.itemPasses ?? prev.itemPasses } : prev,
      );
      setDirty(false);
      setSaveError('');
      return true;
    } catch (e) {
      setSaveError(e instanceof ApiError && e.status === 409 ? t('editor.conflictText') : errorText(e));
      return false;
    }
  }, [doc, id, t]);

  useEffect(() => {
    if (!dirty) return;
    const timer = window.setTimeout(() => void save(), 1200);
    return () => clearTimeout(timer);
  }, [dirty, save]);

  const update = (fn: (p: Playlist) => void): void => {
    if (!doc) return;
    const next = structuredClone(doc);
    fn(next);
    setDoc(next);
    setDirty(true);
  };
  const defaults: Defaults['defaults'] = settings.data?.defaults ?? { mode: 'duration', durationMs: 10000, repeat: 1, fit: 'contain' };
  const tracks = doc?.screens && doc.screens > 1 ? doc.screens : 1;
  const add = (p: Exclude<DragPayload, { move: string }>, at?: number): void => {
    if (tracks > 1 && p.kind === 'presentation') {
      toast(t('playlists.tracks.noPresentations'), 'error');
      return;
    }
    const play = defaultPlay(p.kind, p.mediaKind, defaults, tracks);
    const item: PlaylistItem = { id: newId(), kind: p.kind, refId: p.refId, ...(p.presentationId ? { presentationId: p.presentationId } : {}), play, ...(p.kind === 'media' ? { fit: defaults.fit } : {}) };
    // Only enrich a known name; unknown items get name and kind from the name-resolving effect.
    if (p.mediaKind) setNames((n) => (n[p.refId] ? { ...n, [p.refId]: { ...n[p.refId]!, kind: p.mediaKind } } : n));
    update((d) => d.items.splice(at ?? d.items.length, 0, item));
  };
  const onDrop = (e: React.DragEvent, at: number): void => {
    e.preventDefault();
    setOver(null);
    const raw = e.dataTransfer.getData('application/x-os-item');
    if (!raw) return;
    const p = JSON.parse(raw) as DragPayload;
    if ('move' in p) {
      update((d) => {
        const from = d.items.findIndex((x) => x.id === p.move);
        if (from < 0) return;
        const [it] = d.items.splice(from, 1);
        d.items.splice(from < at ? at - 1 : at, 0, it!);
      });
    } else add(p, at);
  };
  // Cells of further tracks: a slide or a medium; empty = like track 1.
  const setCell = (itemId: string, track: number, p: DragPayload | null): void => {
    if (p && 'move' in p) return;
    if (p && p.kind === 'presentation') {
      toast(t('playlists.tracks.noPresentations'), 'error');
      return;
    }
    if (p?.mediaKind) setNames((n) => (n[p.refId] ? { ...n, [p.refId]: { ...n[p.refId]!, kind: p.mediaKind } } : n));
    update((d) => {
      const it = d.items.find((x) => x.id === itemId);
      if (!it) return;
      if (track === 1) {
        if (!p) return;
        it.kind = p.kind;
        it.refId = p.refId;
        if (p.presentationId) it.presentationId = p.presentationId;
        else delete it.presentationId;
        if (p.kind === 'media') it.fit = it.fit ?? defaults.fit;
        else delete it.fit;
        return;
      }
      const cells = [...(it.cells ?? [])];
      while (cells.length < track - 1) cells.push(null);
      cells[track - 2] = p
        ? { kind: p.kind as PlaylistCell['kind'], refId: p.refId, ...(p.presentationId ? { presentationId: p.presentationId } : {}), ...(p.kind === 'media' ? { fit: defaults.fit } : {}) }
        : null;
      while (cells.length && !cells[cells.length - 1]) cells.pop();
      if (cells.length) it.cells = cells;
      else delete it.cells;
    });
  };
  const setScreens = (n: number): void => {
    if (!doc) return;
    if (n > 1 && doc.items.some((i) => i.kind === 'presentation')) {
      toast(t('playlists.tracks.noPresentations'), 'error');
      return;
    }
    update((d) => {
      if (n > 1) d.screens = n;
      else delete d.screens;
      d.items = d.items.map((it) => {
        const next = n > 1 ? toStep(it, detail?.itemPasses?.[it.id], defaults.durationMs) : { ...it };
        if (n <= 1 || !next.cells) delete next.cells;
        else {
          next.cells = next.cells.slice(0, n - 1);
          if (!next.cells.some(Boolean)) delete next.cells;
        }
        return next;
      });
    });
  };
  const dropOnCell = (e: React.DragEvent, itemId: string, track: number): void => {
    e.preventDefault();
    e.stopPropagation();
    setOver(null);
    const raw = e.dataTransfer.getData('application/x-os-item');
    if (raw) setCell(itemId, track, JSON.parse(raw) as DragPayload);
  };
  const publish = useAction(async () => {
    if (!(await confirm({ title: t('editor.publish'), message: t('playlists.publishConfirm'), confirmLabel: t('editor.publish') }))) return;
    if (dirty && !(await save())) return;
    await post(`/api/v1/playlists/${id}/publish`);
    toast(t('editor.published'));
    load();
  });
  const discard = useAction(async () => {
    if (!(await confirm({ title: t('editor.discard'), message: t('editor.discardConfirm'), danger: true, confirmLabel: t('editor.discard') }))) return;
    await post(`/api/v1/playlists/${id}/discard`);
    load();
  });
  const rename = async (name: string): Promise<void> => {
    if (!detail || name.trim() === detail.name || !name.trim()) return;
    await patch(`/api/v1/playlists/${id}`, { name: name.trim() });
    load();
  };
  if (loadError) return <DataState loading={false} error={loadError} onRetry={load}>{null}</DataState>;
  if (!detail || !doc) return <DataState loading error={null}>{null}</DataState>;
  return (
    <div className="pl-editor">
      <Sidebar onAdd={(p) => add(p)} />
      <div className="pl-main">
        <div className="page-header">
          <div>
            <input className="input pl-title" defaultValue={detail.name} onBlur={(e) => void rename(e.target.value)} />
            <label className="pl-screens">
              <span>{t('playlists.tracks.screens')}</span>
              <select className="input input-sm" value={tracks} onChange={(e) => setScreens(Number(e.target.value))}>
                {Array.from({ length: MAX_PLAYLIST_SCREENS }, (_, i) => i + 1).map((n) => (
                  <option key={n} value={n}>
                    {n === 1 ? t('playlists.tracks.one') : t('playlists.tracks.n', { n })}
                  </option>
                ))}
              </select>
            </label>
            <div className="page-subtitle">
              {t('playlists.runtime', { time: fmtDuration(detail.runtimeMs) })} {!detail.runtimeExact && t('playlists.runtimeApprox')} ·{' '}
              {dirty ? t('editor.unsaved') : t('editor.saved')}{' '}
              {detail.publishedVersion ? (
                detail.hasChanges || dirty ? (
                  <Badge tone="warning">{t('editor.changesNotPublished')}</Badge>
                ) : (
                  <Badge tone="success">{t('pres.published')}</Badge>
                )
              ) : (
                <Badge>{t('editor.neverPublished')}</Badge>
              )}
            </div>
          </div>
          <div className="page-actions">
            <Link className="btn btn-secondary" to="/playlists">
              {t('common.action.back')}
            </Link>
            <Button disabled={!detail.publishedVersion || (!detail.hasChanges && !dirty)} busy={discard.busy} onClick={() => void discard.run()}>
              {t('editor.discard')}
            </Button>
            <Button
              variant="primary"
              icon={<Upload size={16} />}
              busy={publish.busy}
              disabled={!!detail.publishedVersion && !detail.hasChanges && !dirty}
              onClick={() => void publish.run()}
            >
              {t('editor.publish')}
            </Button>
          </div>
        </div>
        {saveError && <div className="form-error">{saveError}</div>}
        <ErrorText error={publish.error ?? discard.error} />
        {tracks > 1 && <p className="faint pl-tracks-hint">{t('playlists.tracks.hint')}</p>}
        <div
          className={`pl-items ${tracks > 1 ? 'pl-steps' : ''}`}
          style={tracks > 1 ? ({ '--tracks': tracks } as React.CSSProperties) : undefined}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => onDrop(e, doc.items.length)}
          onDragLeave={(e) => e.currentTarget === e.target && setOver(null)}
        >
          {doc.items.length === 0 && <div className="state state-empty">{t('playlists.dropHere')}</div>}
          {tracks > 1 && doc.items.length > 0 && (
            <div className="pl-step pl-step-head" aria-hidden="true">
              <span />
              {Array.from({ length: tracks }, (_, n) => (
                <span key={n}>{t('playlists.tracks.track', { n: n + 1 })}</span>
              ))}
              <span>{t('playlists.tracks.duration')}</span>
              <span />
              <span />
            </div>
          )}
          {doc.items.map((it, i) => {
            const info = names[it.refId];
            const Icon = it.kind === 'media' ? (info?.kind === 'video' ? Film : ImageIcon) : it.kind === 'slide' ? ChevronRight : PresIcon;
            if (tracks > 1) {
              const cellView = (track: number) => {
                const c: PlaylistCell | PlaylistItem | null = track === 1 ? it : (it.cells?.[track - 2] ?? null);
                const ci = c ? names[c.refId] : undefined;
                const CIcon = c?.kind === 'media' ? (ci?.kind === 'video' ? Film : ImageIcon) : ChevronRight;
                return (
                  <div key={track} className={`pl-cell ${c ? '' : 'empty'}`} onDragOver={(e) => (e.preventDefault(), e.stopPropagation())} onDrop={(e) => dropOnCell(e, it.id, track)}>
                    {c ? (
                      <>
                        <CIcon size={14} />
                        <span className="pl-name" title={ci?.name}>
                          {ci?.name ?? '…'}
                        </span>
                        {track > 1 && (
                          <IconButton label={t('playlists.tracks.clear')} onClick={() => setCell(it.id, track, null)}>
                            <Trash2 size={13} />
                          </IconButton>
                        )}
                      </>
                    ) : (
                      <span className="faint">{t('playlists.tracks.likeTrack1')}</span>
                    )}
                  </div>
                );
              };
              return (
                <div
                  key={it.id}
                  className={`pl-item pl-step ${over === i ? 'drop' : ''}`}
                  draggable
                  onDragStart={(e) => {
                    e.dataTransfer.setData('application/x-os-item', JSON.stringify({ move: it.id }));
                    e.dataTransfer.effectAllowed = 'move';
                  }}
                  onDragOver={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setOver(i);
                  }}
                  onDrop={(e) => {
                    e.stopPropagation();
                    onDrop(e, i);
                  }}
                >
                  <GripVertical size={16} className="grip" />
                  {Array.from({ length: tracks }, (_, n) => cellView(n + 1))}
                  <Badge>{it.play.mode === 'duration' ? `${Math.round(it.play.ms / 1000)} s` : `${it.play.count}×`}</Badge>
                  <IconButton label={t('common.action.edit')} onClick={() => setEditing(it)}>
                    <Pencil size={15} />
                  </IconButton>
                  <IconButton label={t('common.action.delete')} onClick={() => update((d) => (d.items = d.items.filter((x) => x.id !== it.id)))}>
                    <Trash2 size={15} />
                  </IconButton>
                </div>
              );
            }
            return (
              <div
                key={it.id}
                className={`pl-item ${over === i ? 'drop' : ''}`}
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData('application/x-os-item', JSON.stringify({ move: it.id }));
                  e.dataTransfer.effectAllowed = 'move';
                }}
                onDragOver={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  setOver(i);
                }}
                onDrop={(e) => {
                  e.stopPropagation();
                  onDrop(e, i);
                }}
              >
                <GripVertical size={16} className="grip" />
                <Icon size={16} />
                <span className="pl-name">{info?.name ?? '…'}</span>
                <Badge>{it.play.mode === 'duration' ? `${Math.round(it.play.ms / 1000)} s` : `${it.play.count}×`}</Badge>
                {it.fit && <span className="faint">{t(`admin.fit.${it.fit}`)}</span>}
                <IconButton label={t('common.action.edit')} onClick={() => setEditing(it)}>
                  <Pencil size={15} />
                </IconButton>
                <IconButton label={t('common.action.delete')} onClick={() => update((d) => (d.items = d.items.filter((x) => x.id !== it.id)))}>
                  <Trash2 size={15} />
                </IconButton>
              </div>
            );
          })}
        </div>
      </div>
      {editing && (
        <ItemDialog
          item={editing}
          mediaKind={names[editing.refId]?.kind}
          pass={detail.itemPasses?.[editing.id]}
          defaultMs={defaults.durationMs}
          tracks={tracks}
          onClose={() => setEditing(null)}
          onSave={(next) => {
            update((d) => {
              const i = d.items.findIndex((x) => x.id === next.id);
              if (i >= 0) d.items[i] = next;
            });
            setEditing(null);
          }}
        />
      )}
    </div>
  );
}

function usePlaylistChildren(): NavChild[] {
  const q = useQuery<{ playlists: Summary[] }>('/api/v1/playlists');
  useEvents(['content'], () => q.reload());
  return (q.data?.playlists ?? []).map((p) => ({ key: p.id, label: p.name, path: `/playlists/${p.id}` }));
}

export const playlistsFeature: Feature = {
  routes: [
    { path: '/playlists', element: <PlaylistsPage />, minRole: 'operator' },
    { path: '/playlists/:id', element: <PlaylistEditor />, minRole: 'operator' },
  ],
  nav: [{ key: 'playlists', label: 'admin.nav.playlists', icon: ListVideo, path: '/playlists', minRole: 'operator', order: 40, useChildren: usePlaylistChildren }],
};
