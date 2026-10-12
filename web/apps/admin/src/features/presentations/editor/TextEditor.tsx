// In-place rich text editing with ProseMirror. The editing view uses the same box, base
// style and span styles as the renderer, so text does not jump when editing starts.
import { useEffect, useRef, useSyncExternalStore } from 'react';
import { EditorState, TextSelection } from 'prosemirror-state';
import { EditorView } from 'prosemirror-view';
import { baseKeymap, toggleMark } from 'prosemirror-commands';
import { history, redo, undo } from 'prosemirror-history';
import { keymap } from 'prosemirror-keymap';
import type { Marks, RichDoc, TextElement } from '@opensignage/contracts';
import { applyTextStyle, color, px } from '@opensignage/render';
import { fromPM, richSchema, toPM } from './richtext';
import type { EditorStore } from './store';

/** The active text editing session (the panel formats the selection through it). */
export const textSession: { view: EditorView | null } = { view: null };
const sessionListeners = new Set<() => void>();
let sessionVersion = 0;

function notifySession(): void {
  sessionVersion++;
  for (const l of sessionListeners) l();
}

function setSession(view: EditorView | null): void {
  textSession.view = view;
  notifySession();
}

const subscribeSession = (cb: () => void): (() => void) => {
  sessionListeners.add(cb);
  return () => sessionListeners.delete(cb);
};

/** Re-renders when text editing starts or ends and when its text or selection changes
 *  (the panel shows the formatting at the cursor). */
export function useTextEditing(): boolean {
  useSyncExternalStore(subscribeSession, () => sessionVersion);
  return textSession.view !== null;
}

/** Value of a yes/no mark over the selection while editing: the mark where it is set, the
 *  element's own style (base) elsewhere; true only when it holds for all marked text. */
export function selectionMark(name: 'bold' | 'italic' | 'underline' | 'strike', base: boolean): boolean {
  const view = textSession.view;
  if (!view) return base;
  const type = richSchema.marks[name];
  if (!type) return base;
  const { from, to, empty, $from } = view.state.selection;
  if (empty) {
    const m = type.isInSet(view.state.storedMarks ?? $from.marks());
    return m ? !!m.attrs.value : base;
  }
  let all = true;
  let any = false;
  view.state.doc.nodesBetween(from, to, (node) => {
    if (!node.isText) return;
    any = true;
    const m = type.isInSet(node.marks);
    if (!(m ? !!m.attrs.value : base)) all = false;
  });
  return any ? all : base;
}

/** Applies (or removes with null) a mark on the current text selection. */
export function applyTextMark(name: keyof Marks, value: unknown): boolean {
  const view = textSession.view;
  if (!view) return false;
  const type = richSchema.marks[name];
  if (!type) return false;
  const { from, to, empty } = view.state.selection;
  let tr = view.state.tr;
  if (empty) {
    tr = value === null ? tr.removeStoredMark(type) : tr.addStoredMark(type.create({ value }));
  } else {
    tr = tr.removeMark(from, to, type);
    if (value !== null) tr = tr.addMark(from, to, type.create({ value }));
  }
  view.dispatch(tr);
  view.focus();
  return true;
}

/** Sets paragraph attributes (align, bullet) for the selected paragraphs. */
export function applyParagraph(attrs: { align?: string | null; bullet?: string | null }): boolean {
  const view = textSession.view;
  if (!view) return false;
  const { from, to } = view.state.selection;
  let tr = view.state.tr;
  view.state.doc.nodesBetween(from, to, (node, pos) => {
    if (node.type.name === 'paragraph') tr = tr.setNodeMarkup(pos, undefined, { ...node.attrs, ...attrs });
    return false;
  });
  view.dispatch(tr);
  view.focus();
  return true;
}

function boolToggle(name: 'bold' | 'italic' | 'underline' | 'strike') {
  return (state: EditorState, dispatch?: EditorView['dispatch']): boolean => {
    const type = richSchema.marks[name]!;
    const { from, to } = state.selection;
    const has = state.doc.rangeHasMark(from, to, type);
    return toggleMark(type, { value: !has })(state, dispatch);
  };
}

export function TextEditor({
  element,
  scale,
  onChange,
  onDone,
  store,
}: {
  element: TextElement;
  scale: number;
  onChange: (doc: RichDoc) => void;
  onDone: () => void;
  store: EditorStore;
}) {
  const host = useRef<HTMLDivElement>(null);
  const changed = useRef(false);
  const doneRef = useRef(onDone);
  doneRef.current = onDone;
  const changeRef = useRef(onChange);
  changeRef.current = onChange;

  useEffect(() => {
    const el = host.current;
    if (!el) return;
    applyTextStyle(el, element.style);
    if (element.fill) el.style.backgroundColor = color(element.fill);
    el.style.padding = (element.padding ?? [0, 0, 0, 0]).map(px).join(' ');
    const finish = (): void => {
      if (changed.current && textSession.view) changeRef.current(fromPM(textSession.view.state.doc));
      doneRef.current();
    };
    const state = EditorState.create({
      doc: toPM(element.doc),
      plugins: [
        history(),
        keymap({
          'Mod-z': undo,
          'Mod-y': redo,
          'Mod-Shift-z': redo,
          'Mod-b': boolToggle('bold'),
          'Mod-i': boolToggle('italic'),
          'Mod-u': boolToggle('underline'),
          Escape: () => {
            finish();
            return true;
          },
        }),
        keymap(baseKeymap),
      ],
    });
    const view = new EditorView(el, {
      state,
      dispatchTransaction(tr) {
        const next = view.state.apply(tr);
        view.updateState(next);
        if (tr.docChanged) changed.current = true;
        notifySession();
      },
      handleDOMEvents: {
        blur: (_v, ev) => {
          const to = (ev as FocusEvent).relatedTarget as HTMLElement | null;
          // Clicking the toolbar or the panel keeps editing (formatting the selection).
          if (to && to.closest('.editor-toolbar, .editor-panel, .dialog')) return false;
          finish();
          return false;
        },
      },
    });
    setSession(view);
    view.focus();
    view.dispatch(view.state.tr.setSelection(TextSelection.atEnd(view.state.doc)));
    return () => {
      if (changed.current) changeRef.current(fromPM(view.state.doc));
      setSession(null);
      view.destroy();
    };
    // The editor is created once per editing session.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [element.id]);

  void store;
  return (
    <div
      className="text-editing os-text"
      data-valign={element.valign}
      ref={host}
      style={{
        left: element.x * scale,
        top: element.y * scale,
        width: element.w * scale,
        height: element.h * scale,
        transform: element.rotation ? `rotate(${element.rotation}deg)` : undefined,
      }}
      onPointerDown={(e) => e.stopPropagation()}
    />
  );
}
