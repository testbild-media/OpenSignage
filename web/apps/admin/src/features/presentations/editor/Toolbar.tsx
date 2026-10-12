// Editor toolbar: one compact row of symbol buttons (tooltips and screen readers name them) –
// edit, insert, arrange, view. Formatting and all other properties of the selection are in
// the panel on the right (Panel.tsx), so the toolbar never changes its height.
import { useRef, useState, type ReactNode } from 'react';
import {
  AlignCenterHorizontal,
  AlignCenterVertical,
  AlignEndHorizontal,
  AlignEndVertical,
  AlignHorizontalDistributeCenter,
  AlignStartHorizontal,
  AlignStartVertical,
  AlignVerticalDistributeCenter,
  ArrowRight,
  BringToFront,
  ChevronDown,
  Circle,
  ClipboardPaste,
  Clock,
  Copy,
  CopyPlus,
  Grid3x3,
  Group,
  Image,
  LayoutList,
  Magnet,
  Minus,
  Music,
  Package,
  Pin,
  Redo2,
  Rows3,
  Ruler,
  Scissors,
  SendToBack,
  Shapes,
  Square,
  Stamp,
  Trash2,
  Type,
  Undo2,
  Ungroup,
  Video,
} from 'lucide-react';
import { useT } from '../../../lib/hooks';
import type { EditorStore } from './store';
import { useEditor } from './useEditor';
import { createElement, type InsertKind } from './factory';
import { MediaPickerDialog } from './pickers';
import { Num } from './controls';
import { ContextMenu, type MenuEntry, type MenuState } from './ContextMenu';

function TB({ label, onClick, children, active, disabled }: { label: string; onClick: () => void; children: ReactNode; active?: boolean; disabled?: boolean }) {
  return (
    <button
      type="button"
      className={`tb ${active ? 'active' : ''}`}
      title={label}
      aria-label={label}
      aria-pressed={active}
      onClick={onClick}
      disabled={disabled}
      // Keeps the text selection while a text is being edited.
      onMouseDown={(e) => e.preventDefault()}
    >
      {children}
    </button>
  );
}

/** Button that opens a menu below itself (shapes, align, zoom). */
function TBMenu({ label, items, children, text, disabled }: { label: string; items: () => MenuEntry[]; children?: ReactNode; text?: string; disabled?: boolean }) {
  const t = useT();
  const ref = useRef<HTMLButtonElement>(null);
  const [menu, setMenu] = useState<MenuState | null>(null);
  return (
    <>
      <button
        ref={ref}
        type="button"
        className={`tb tb-menu ${menu ? 'active' : ''}`}
        title={label}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={!!menu}
        disabled={disabled}
        onMouseDown={(e) => e.preventDefault()}
        onClick={() => {
          const r = ref.current?.getBoundingClientRect();
          if (r) setMenu({ x: Math.round(r.left), y: Math.round(r.bottom + 2), items: items() });
        }}
      >
        {children}
        {text && <span className="tb-text">{text}</span>}
        <ChevronDown size={12} className="tb-caret" />
      </button>
      {menu && <ContextMenu menu={menu} onClose={() => setMenu(null)} label={label || t('editor.menu.label')} />}
    </>
  );
}

const Sep = () => <span className="tb-sep" aria-hidden="true" />;

export function Toolbar({ store }: { store: EditorStore }) {
  const t = useT();
  const snap = useEditor(store);
  const [pick, setPick] = useState<null | { kind: 'image' | 'video' | 'audio'; insert: InsertKind }>(null);
  const slide = snap.doc.slides.find((s) => s.id === snap.slideId);
  const sel = slide?.elements.filter((e) => snap.selection.includes(e.id)) ?? [];

  // Media limits (one video, one audio element per slide) are enforced by the store, which
  // reports a refusal through onLimit (toast in the editor page).
  const insert = (kind: InsertKind, assetId?: string): void => {
    if (!slide) return;
    store.addElement(createElement(kind, snap.doc, { assetId }));
  };
  const doInsert = (kind: InsertKind): void => {
    const k = kind === 'image' || kind === 'logo' ? 'image' : kind === 'video' ? 'video' : kind === 'audio' ? 'audio' : null;
    // Refuse before the file picker opens, not after a file was chosen.
    if ((kind === 'video' || kind === 'audio') && !store.checkLimit([{ type: kind }])) return;
    if (k) setPick({ kind: k, insert: kind });
    else insert(kind);
  };

  const shapes = (): MenuEntry[] =>
    (
      [
        ['rect', <Square size={16} />],
        ['roundrect', <Square size={16} className="rounded-icon" />],
        ['ellipse', <Circle size={16} />],
        ['line', <Minus size={16} />],
        ['arrow', <ArrowRight size={16} />],
      ] as const
    ).map(([k, icon]) => ({ label: t(`editor.shape.${k}`), icon, onSelect: () => doInsert(k) }));

  const alignMenu = (): MenuEntry[] => {
    const one = sel.length < 1;
    const three = sel.length < 3;
    return [
      { label: t('editor.alignLeft'), icon: <AlignStartVertical size={16} />, disabled: one, onSelect: () => store.align('left') },
      { label: t('editor.alignCenter'), icon: <AlignCenterVertical size={16} />, disabled: one, onSelect: () => store.align('center') },
      { label: t('editor.alignRight'), icon: <AlignEndVertical size={16} />, disabled: one, onSelect: () => store.align('right') },
      'separator',
      { label: t('editor.alignTop'), icon: <AlignStartHorizontal size={16} />, disabled: one, onSelect: () => store.align('top') },
      { label: t('editor.alignMiddle'), icon: <AlignCenterHorizontal size={16} />, disabled: one, onSelect: () => store.align('middle') },
      { label: t('editor.alignBottom'), icon: <AlignEndHorizontal size={16} />, disabled: one, onSelect: () => store.align('bottom') },
      'separator',
      { label: t('editor.distributeH'), icon: <AlignHorizontalDistributeCenter size={16} />, disabled: three, onSelect: () => store.distribute('h') },
      { label: t('editor.distributeV'), icon: <AlignVerticalDistributeCenter size={16} />, disabled: three, onSelect: () => store.distribute('v') },
    ];
  };

  const zoom = snap.view.zoom;
  const zoomMenu = (): MenuEntry[] => [
    { label: t('editor.zoomFit'), checked: zoom === 'fit', onSelect: () => store.setView({ zoom: 'fit' }) },
    ...[0.25, 0.5, 1, 2].map((z) => ({ label: `${z * 100} %`, checked: zoom === z, onSelect: () => store.setView({ zoom: z }) })),
  ];

  return (
    <div className="editor-toolbar" role="toolbar" aria-label={t('editor.toolbar')}>
      <TB label={t('editor.undo')} onClick={() => store.undo()} disabled={!snap.canUndo}>
        <Undo2 size={17} />
      </TB>
      <TB label={t('editor.redo')} onClick={() => store.redo()} disabled={!snap.canRedo}>
        <Redo2 size={17} />
      </TB>
      <Sep />
      <TB label={t('editor.cut')} onClick={() => store.cut()} disabled={!sel.length}>
        <Scissors size={17} />
      </TB>
      <TB label={t('editor.copy')} onClick={() => store.copy()} disabled={!sel.length}>
        <Copy size={17} />
      </TB>
      <TB label={t('editor.paste')} onClick={() => store.paste()}>
        <ClipboardPaste size={17} />
      </TB>
      <TB label={t('editor.duplicate')} onClick={() => store.duplicate()} disabled={!sel.length}>
        <CopyPlus size={17} />
      </TB>
      <TB label={t('common.action.delete')} onClick={() => store.deleteSelection()} disabled={!sel.length}>
        <Trash2 size={17} />
      </TB>
      <Sep />
      <TB label={t('editor.el.text')} onClick={() => doInsert('text')}>
        <Type size={17} />
      </TB>
      <TB label={t('editor.el.image')} onClick={() => doInsert('image')}>
        <Image size={17} />
      </TB>
      <TB label={t('editor.el.video')} onClick={() => doInsert('video')}>
        <Video size={17} />
      </TB>
      <TB label={t('editor.el.audio')} onClick={() => doInsert('audio')}>
        <Music size={17} />
      </TB>
      <TB label={t('editor.el.logo')} onClick={() => doInsert('logo')}>
        <Stamp size={17} />
      </TB>
      <TBMenu label={t('editor.group.shapes')} items={shapes}>
        <Shapes size={17} />
      </TBMenu>
      <Sep />
      <TB label={t('editor.el.productList')} onClick={() => doInsert('productList')}>
        <LayoutList size={17} />
      </TB>
      <TB label={t('editor.el.product')} onClick={() => doInsert('product')}>
        <Package size={17} />
      </TB>
      <TB label={t('editor.el.clock')} onClick={() => doInsert('clock')}>
        <Clock size={17} />
      </TB>
      <TB label={t('editor.el.ticker')} onClick={() => doInsert('ticker')}>
        <Rows3 size={17} />
      </TB>
      <Sep />
      <TB label={t('editor.front')} onClick={() => store.arrange('front')} disabled={!sel.length}>
        <BringToFront size={17} />
      </TB>
      <TB label={t('editor.back')} onClick={() => store.arrange('back')} disabled={!sel.length}>
        <SendToBack size={17} />
      </TB>
      <TBMenu label={t('editor.group.align')} items={alignMenu} disabled={!sel.length}>
        <AlignStartVertical size={17} />
      </TBMenu>
      <TB label={t('editor.group')} onClick={() => store.group()} disabled={sel.length < 2}>
        <Group size={17} />
      </TB>
      <TB label={t('editor.ungroup')} onClick={() => store.ungroup()} disabled={!sel.some((e) => e.groupId)}>
        <Ungroup size={17} />
      </TB>
      <TB label={t('editor.copyToAll')} onClick={() => store.copyToAllSlides()} disabled={!sel.length}>
        <Pin size={17} />
      </TB>
      <span className="tb-spacer" />
      <TB label={t('editor.grid')} active={snap.view.grid} onClick={() => store.setView({ grid: !snap.view.grid })}>
        <Grid3x3 size={17} />
      </TB>
      {snap.view.grid && (
        <span className="tb-num" title={t('editor.gridSize')}>
          <Num value={snap.view.gridSize} min={2} max={500} onChange={(v) => store.setView({ gridSize: v })} />
        </span>
      )}
      <TB label={t('editor.snap')} active={snap.view.snap} onClick={() => store.setView({ snap: !snap.view.snap })}>
        <Magnet size={17} />
      </TB>
      <TB label={t('editor.rulers')} active={snap.view.rulers} onClick={() => store.setView({ rulers: !snap.view.rulers })}>
        <Ruler size={17} />
      </TB>
      <TBMenu label={t('editor.group.zoom')} items={zoomMenu} text={zoom === 'fit' ? t('editor.zoomFit') : `${zoom * 100} %`} />
      {pick && (
        <MediaPickerDialog
          kind={pick.kind}
          onClose={() => setPick(null)}
          onPick={(id) => {
            insert(pick.insert, id);
            setPick(null);
          }}
        />
      )}
    </div>
  );
}
