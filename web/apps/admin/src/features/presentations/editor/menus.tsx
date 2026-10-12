// Context menu contents. Every entry calls the same store command as the toolbar or the
// keyboard shortcut, so all paths share undo, limits and selection rules.
import {
  AlignCenterHorizontal,
  AlignCenterVertical,
  AlignEndHorizontal,
  AlignEndVertical,
  AlignHorizontalDistributeCenter,
  AlignStartHorizontal,
  AlignStartVertical,
  AlignVerticalDistributeCenter,
  ArrowDown,
  ArrowUp,
  BringToFront,
  ClipboardPaste,
  Copy,
  CopyPlus,
  Eye,
  EyeOff,
  Group,
  Layers,
  Lock,
  LockOpen,
  MoveDown,
  MoveUp,
  PaintBucket,
  Pencil,
  Plus,
  Scissors,
  SendToBack,
  SquareDashedMousePointer,
  Trash2,
  Type,
  Ungroup,
} from 'lucide-react';
import type { Slide } from '@opensignage/contracts';
import type { t as translate } from '@opensignage/i18n';
import type { EditorStore } from './store';
import type { MenuEntry } from './ContextMenu';
import type { GuideAxis } from './guides';

type T = typeof translate;
const I = 15; // icon size

/** Menu for the selected elements (canvas and layer list). */
export function elementMenu(store: EditorStore, t: T, opts: { editText?: () => void } = {}): MenuEntry[] {
  const sel = store.selected;
  const ids = sel.map((e) => e.id);
  const none = sel.length === 0;
  const movable = sel.filter((e) => !e.locked).length > 0;
  const allLocked = !none && sel.every((e) => e.locked);
  const allPinned = !none && sel.every((e) => e.pinned);
  const entries: MenuEntry[] = [];
  if (opts.editText) entries.push({ label: t('editor.menu.editText'), icon: <Type size={I} />, onSelect: opts.editText }, 'separator');
  entries.push(
    { label: t('editor.cut'), icon: <Scissors size={I} />, shortcut: 'Ctrl+X', disabled: !movable, onSelect: () => store.cut() },
    { label: t('editor.copy'), icon: <Copy size={I} />, shortcut: 'Ctrl+C', disabled: none, onSelect: () => store.copy() },
    { label: t('editor.paste'), icon: <ClipboardPaste size={I} />, shortcut: 'Ctrl+V', disabled: !store.canPaste(), onSelect: () => store.paste() },
    { label: t('editor.duplicate'), icon: <CopyPlus size={I} />, shortcut: 'Ctrl+D', disabled: none, onSelect: () => store.duplicate() },
    { label: t('common.action.delete'), icon: <Trash2 size={I} />, shortcut: 'Del', danger: true, disabled: !movable, onSelect: () => store.deleteSelection() },
    'separator',
    { label: t('editor.front'), icon: <BringToFront size={I} />, disabled: none, onSelect: () => store.arrange('front') },
    { label: t('editor.menu.forward'), icon: <ArrowUp size={I} />, disabled: none, onSelect: () => store.arrange('forward') },
    { label: t('editor.menu.backward'), icon: <ArrowDown size={I} />, disabled: none, onSelect: () => store.arrange('backward') },
    { label: t('editor.back'), icon: <SendToBack size={I} />, disabled: none, onSelect: () => store.arrange('back') },
    'separator',
    {
      label: t('editor.group.align'),
      icon: <AlignStartVertical size={I} />,
      disabled: !movable,
      submenu: [
        { label: t('editor.alignLeft'), icon: <AlignStartVertical size={I} />, onSelect: () => store.align('left') },
        { label: t('editor.alignCenter'), icon: <AlignCenterVertical size={I} />, onSelect: () => store.align('center') },
        { label: t('editor.alignRight'), icon: <AlignEndVertical size={I} />, onSelect: () => store.align('right') },
        'separator',
        { label: t('editor.alignTop'), icon: <AlignStartHorizontal size={I} />, onSelect: () => store.align('top') },
        { label: t('editor.alignMiddle'), icon: <AlignCenterHorizontal size={I} />, onSelect: () => store.align('middle') },
        { label: t('editor.alignBottom'), icon: <AlignEndHorizontal size={I} />, onSelect: () => store.align('bottom') },
        'separator',
        { label: t('editor.distributeH'), icon: <AlignHorizontalDistributeCenter size={I} />, disabled: sel.length < 3, onSelect: () => store.distribute('h') },
        { label: t('editor.distributeV'), icon: <AlignVerticalDistributeCenter size={I} />, disabled: sel.length < 3, onSelect: () => store.distribute('v') },
      ],
    },
    'separator',
    { label: t('editor.group'), icon: <Group size={I} />, shortcut: 'Ctrl+G', disabled: sel.length < 2, onSelect: () => store.group() },
    { label: t('editor.ungroup'), icon: <Ungroup size={I} />, shortcut: 'Ctrl+Shift+G', disabled: !sel.some((e) => e.groupId), onSelect: () => store.ungroup() },
    'separator',
    {
      label: allLocked ? t('editor.menu.unlock') : t('editor.menu.lock'),
      icon: allLocked ? <LockOpen size={I} /> : <Lock size={I} />,
      disabled: none,
      onSelect: () =>
        store.updateElements(ids, (e) => {
          e.locked = !allLocked;
        }),
    },
    {
      label: t('editor.prop.pinned'),
      checked: allPinned,
      disabled: none,
      onSelect: () =>
        store.updateElements(ids, (e) => {
          e.pinned = !allPinned;
        }),
    },
    { label: t('editor.copyToAll'), icon: <Layers size={I} />, disabled: none, onSelect: () => store.copyToAllSlides() },
  );
  return entries;
}

/** Menu for the empty slide area. */
export function canvasMenu(store: EditorStore, t: T, opts: { background: () => void }): MenuEntry[] {
  const { view, guides } = store.getSnapshot();
  const selectable = store.slide?.elements.some((e) => !e.locked && !e.hidden) ?? false;
  return [
    { label: t('editor.paste'), icon: <ClipboardPaste size={I} />, shortcut: 'Ctrl+V', disabled: !store.canPaste(), onSelect: () => store.paste() },
    { label: t('editor.menu.selectAll'), icon: <SquareDashedMousePointer size={I} />, shortcut: 'Ctrl+A', disabled: !selectable, onSelect: () => store.selectAll() },
    'separator',
    { label: t('editor.menu.background'), icon: <PaintBucket size={I} />, onSelect: opts.background },
    'separator',
    { label: t('editor.grid'), checked: view.grid, onSelect: () => store.setView({ grid: !view.grid }) },
    { label: t('editor.snap'), checked: view.snap, onSelect: () => store.setView({ snap: !view.snap }) },
    { label: t('editor.rulers'), checked: view.rulers, onSelect: () => store.setView({ rulers: !view.rulers }) },
    { label: t('editor.menu.clearGuides'), icon: <Trash2 size={I} />, disabled: guides.x.length + guides.y.length === 0, onSelect: () => store.clearGuides() },
  ];
}

/** Menu for one guide line. */
export function guideMenu(store: EditorStore, t: T, axis: GuideAxis, index: number): MenuEntry[] {
  return [
    { label: t('editor.menu.deleteGuide'), icon: <Trash2 size={I} />, onSelect: () => store.removeGuide(axis, index) },
    { label: t('editor.menu.clearGuides'), icon: <Trash2 size={I} />, onSelect: () => store.clearGuides() },
  ];
}

/** Menu for a slide in the slide rail. */
export function slideMenu(
  store: EditorStore,
  t: T,
  slide: Slide,
  opts: { newSlide: () => void; rename: () => void; remove: () => void; background: () => void },
): MenuEntry[] {
  const slides = store.presentation.slides;
  const i = slides.findIndex((s) => s.id === slide.id);
  return [
    { label: t('editor.slide.add'), icon: <Plus size={I} />, onSelect: opts.newSlide },
    { label: t('editor.duplicate'), icon: <CopyPlus size={I} />, onSelect: () => store.duplicateSlide(slide.id) },
    { label: t('editor.menu.rename'), icon: <Pencil size={I} />, onSelect: opts.rename },
    {
      label: slide.hidden ? t('editor.slide.show') : t('editor.slide.hide'),
      icon: slide.hidden ? <Eye size={I} /> : <EyeOff size={I} />,
      onSelect: () => store.updateSlide(slide.id, (s) => (s.hidden = !s.hidden)),
    },
    { label: t('editor.menu.background'), icon: <PaintBucket size={I} />, onSelect: opts.background },
    'separator',
    { label: t('editor.menu.moveUp'), icon: <MoveUp size={I} />, disabled: i <= 0, onSelect: () => store.moveSlide(slide.id, i - 1) },
    { label: t('editor.menu.moveDown'), icon: <MoveDown size={I} />, disabled: i < 0 || i >= slides.length - 1, onSelect: () => store.moveSlide(slide.id, i + 1) },
    'separator',
    { label: t('editor.slide.delete'), icon: <Trash2 size={I} />, danger: true, disabled: slides.length <= 1, onSelect: opts.remove },
  ];
}

/** Menu for the free area of the slide rail. */
export function railMenu(t: T, newSlide: () => void): MenuEntry[] {
  return [{ label: t('editor.slide.add'), icon: <Plus size={I} />, onSelect: newSlide }];
}
