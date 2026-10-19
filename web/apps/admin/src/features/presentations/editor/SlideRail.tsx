// Slide rail with real thumbnails (same renderer as the player), drag to reorder,
// actions: rename, duplicate, hide, delete, move – as hover buttons and as context menu.
import { useEffect, useRef, useState } from 'react';
import { Copy, EyeOff, Eye, Plus, Trash2 } from 'lucide-react';
import type { Slide } from '@opensignage/contracts';
import { renderThumbnail, type RenderEnv } from '@opensignage/render';
import { useT } from '../../../lib/hooks';
import { useConfirm } from '../../../components/feedback';
import type { EditorStore } from './store';
import { useEditor } from './useEditor';
import { createSlide, type ContentDefaults } from './factory';
import { ContextMenu, menuPointFor, type MenuState } from './ContextMenu';
import { railMenu, slideMenu } from './menus';
import { SlideBackgroundDialog } from './Panel';

function Thumb({ slide, width, height, env }: { slide: Slide; width: number; height: number; env: RenderEnv }) {
  const ref = useRef<HTMLDivElement>(null);
  const json = JSON.stringify(slide);
  useEffect(() => {
    if (!ref.current) return;
    return renderThumbnail(ref.current, slide, width, height, env);
    // Re-render only when the slide content changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [json, width, height, env]);
  return <div className="thumb" ref={ref} />;
}

/** Screen point for a context menu event (keyboard-opened menus sit on the element). */
function menuPoint(ev: React.MouseEvent): { x: number; y: number } {
  const native = ev.nativeEvent as MouseEvent & { pointerType?: string };
  if (native.pointerType === '' || (ev.clientX === 0 && ev.clientY === 0)) return menuPointFor(ev.currentTarget);
  return { x: ev.clientX, y: ev.clientY };
}

export function SlideRail({ store, env, defaults }: { store: EditorStore; env: RenderEnv; defaults: ContentDefaults }) {
  const t = useT();
  const confirm = useConfirm();
  const snap = useEditor(store);
  const [dragId, setDragId] = useState<string | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);
  const [bgSlide, setBgSlide] = useState<string | null>(null);
  const itemRefs = useRef(new Map<string, HTMLDivElement>());
  const { doc } = snap;
  const remove = async (s: Slide): Promise<void> => {
    if (doc.slides.length <= 1) return;
    const ok = await confirm({ title: t('editor.slide.delete'), message: t('editor.slide.deleteConfirm', { name: s.name || t('editor.slide.number', { n: doc.slides.indexOf(s) + 1 }) }), danger: true, confirmLabel: t('common.action.delete') });
    if (ok) store.deleteSlide(s.id);
  };
  const newSlide = (afterId: string): void => store.addSlide(createSlide(defaults), afterId);
  const openSlideMenu = (ev: React.MouseEvent, s: Slide): void => {
    ev.preventDefault();
    ev.stopPropagation();
    store.selectSlide(s.id);
    setMenu({
      ...menuPoint(ev),
      items: slideMenu(store, t, s, {
        newSlide: () => newSlide(s.id),
        rename: () => setRenaming(s.id),
        remove: () => void remove(s),
        background: () => setBgSlide(s.id),
      }),
    });
  };
  // Keep the keyboard focus on the current slide after moving it with the menu.
  const focusSlide = (id: string): void => itemRefs.current.get(id)?.focus();
  return (
    <div
      className="slide-rail"
      onDragEnd={() => (setDragId(null), setOver(null))}
      onContextMenu={(ev) => {
        ev.preventDefault();
        setMenu({ ...menuPoint(ev), items: railMenu(t, () => newSlide(snap.slideId)) });
      }}
    >
      {doc.slides.map((s, i) => (
        <div
          key={s.id}
          ref={(el) => {
            if (el) itemRefs.current.set(s.id, el);
            else itemRefs.current.delete(s.id);
          }}
          className={`rail-item ${s.id === snap.slideId ? 'active' : ''} ${s.id === snap.slideId && snap.focus === 'slide' && !snap.selection.length ? 'selected' : ''} ${s.hidden ? 'is-hidden' : ''} ${over === i ? 'drop' : ''}`}
          draggable
          tabIndex={0}
          aria-current={s.id === snap.slideId || undefined}
          aria-label={s.name || t('editor.slide.number', { n: i + 1 })}
          onDragStart={() => setDragId(s.id)}
          onDragOver={(e) => {
            e.preventDefault();
            setOver(i);
          }}
          onDrop={(e) => {
            e.preventDefault();
            if (dragId) store.moveSlide(dragId, i);
            setDragId(null);
            setOver(null);
          }}
          onClick={() => store.selectSlide(s.id)}
          onContextMenu={(ev) => openSlideMenu(ev, s)}
          onKeyDown={(e) => {
            if (e.target !== e.currentTarget) return;
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              store.selectSlide(s.id);
            } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              // Up/down walk through the slides like in the slide sorter.
              e.preventDefault();
              e.stopPropagation();
              const next = doc.slides[i + (e.key === 'ArrowDown' ? 1 : -1)];
              if (next) {
                store.selectSlide(next.id);
                focusSlide(next.id);
              }
            }
          }}
        >
          <div className="rail-num">{i + 1}</div>
          <div className="rail-main">
            <Thumb slide={s} width={doc.width} height={doc.height} env={env} />
            {renaming === s.id ? (
              <input
                className="input input-sm"
                autoFocus
                defaultValue={s.name}
                onClick={(e) => e.stopPropagation()}
                onBlur={(e) => {
                  const v = e.target.value.trim();
                  if (v !== s.name) store.updateSlide(s.id, (x) => (x.name = v));
                  setRenaming(null);
                }}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur();
                  if (e.key === 'Escape') setRenaming(null);
                }}
              />
            ) : (
              <div className="rail-name" onDoubleClick={() => setRenaming(s.id)} title={t('editor.slide.renameHint')}>
                {s.name || t('editor.slide.number', { n: i + 1 })}
              </div>
            )}
          </div>
          <div className="rail-actions">
            <button type="button" className="icon-btn" tabIndex={-1} title={t('editor.duplicate')} onClick={(e) => (e.stopPropagation(), store.duplicateSlide(s.id))}>
              <Copy size={14} />
            </button>
            <button type="button" className="icon-btn" tabIndex={-1} title={s.hidden ? t('editor.slide.show') : t('editor.slide.hide')} onClick={(e) => (e.stopPropagation(), store.updateSlide(s.id, (x) => (x.hidden = !x.hidden)))}>
              {s.hidden ? <EyeOff size={14} /> : <Eye size={14} />}
            </button>
            <button type="button" className="icon-btn" tabIndex={-1} title={t('common.action.delete')} disabled={doc.slides.length <= 1} onClick={(e) => (e.stopPropagation(), void remove(s))}>
              <Trash2 size={14} />
            </button>
          </div>
        </div>
      ))}
      <button type="button" className="rail-add" onClick={() => newSlide(snap.slideId)}>
        <Plus size={16} /> {t('editor.slide.add')}
      </button>
      {menu && <ContextMenu menu={menu} onClose={() => setMenu(null)} label={t('editor.menu.label')} />}
      {bgSlide && <SlideBackgroundDialog store={store} slideId={bgSlide} onClose={() => setBgSlide(null)} />}
    </div>
  );
}
