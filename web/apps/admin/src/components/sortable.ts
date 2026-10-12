// Drag & drop sorting for lists and table rows. Only the grip handle starts a drag (no
// accidental drags from buttons, no text selection); the drop marker lives in one central
// state that is always reset on drag end. Arrow keys on the handle move
// an entry by one position for keyboard users.
import { type DragEvent, type KeyboardEvent, useCallback, useRef, useState } from 'react';

export type DropPosition = 'before' | 'after';

/** Moves id before/after target and returns the new order (pure). */
export function moveId(ids: string[], id: string, target: string, pos: DropPosition): string[] {
  if (id === target || !ids.includes(id) || !ids.includes(target)) return ids;
  const rest = ids.filter((x) => x !== id);
  const at = rest.indexOf(target) + (pos === 'after' ? 1 : 0);
  return [...rest.slice(0, at), id, ...rest.slice(at)];
}

/** Moves id by delta positions (pure, clamped). */
export function shiftId(ids: string[], id: string, delta: number): string[] {
  const i = ids.indexOf(id);
  if (i < 0) return ids;
  const j = Math.max(0, Math.min(ids.length - 1, i + delta));
  if (i === j) return ids;
  const out = ids.filter((x) => x !== id);
  out.splice(j, 0, id);
  return out;
}

interface DragState {
  id: string | null;
  over: string | null;
  pos: DropPosition;
}

const IDLE: DragState = { id: null, over: null, pos: 'before' };

/**
 * ids: current order (one sibling group). onReorder receives the new order; it is only
 * called when the order really changed.
 */
export function useSortable(ids: string[], onReorder: (next: string[]) => void, disabled = false) {
  const [drag, setDrag] = useState<DragState>(IDLE);
  const dragRef = useRef<DragState>(IDLE);
  const set = (d: DragState): void => {
    dragRef.current = d;
    setDrag(d);
  };
  const idsKey = ids.join(',');
  const reset = useCallback(() => {
    dragRef.current = IDLE;
    setDrag(IDLE);
  }, []);

  const commit = (next: string[]): void => {
    if (next.join(',') !== idsKey) onReorder(next);
  };

  const itemProps = (id: string) => ({
    'data-sortable-item': id,
    'data-sort-dragging': drag.id === id ? 'true' : undefined,
    'data-sort-drop': drag.id && drag.over === id && drag.id !== id ? drag.pos : undefined,
    onDragOver: (e: DragEvent<HTMLElement>) => {
      const cur = dragRef.current;
      if (!cur.id || !ids.includes(cur.id)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      const r = e.currentTarget.getBoundingClientRect();
      const pos: DropPosition = e.clientY < r.top + r.height / 2 ? 'before' : 'after';
      if (cur.over !== id || cur.pos !== pos) set({ ...cur, over: id, pos });
    },
    onDrop: (e: DragEvent<HTMLElement>) => {
      const cur = dragRef.current;
      if (!cur.id || !ids.includes(cur.id)) return;
      e.preventDefault();
      const r = e.currentTarget.getBoundingClientRect();
      const pos: DropPosition = e.clientY < r.top + r.height / 2 ? 'before' : 'after';
      reset();
      commit(moveId(ids, cur.id, id, pos));
    },
  });

  const handleProps = (id: string, label: string) => ({
    draggable: !disabled,
    disabled,
    title: label,
    'aria-label': label,
    className: 'drag-handle',
    onDragStart: (e: DragEvent<HTMLElement>) => {
      const row = e.currentTarget.closest<HTMLElement>('[data-sortable-item]');
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', id);
      if (row) {
        const r = row.getBoundingClientRect();
        e.dataTransfer.setDragImage(row, e.clientX - r.left, e.clientY - r.top);
      }
      set({ id, over: null, pos: 'before' });
    },
    onDragEnd: reset,
    onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
      if (disabled) return;
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        e.preventDefault();
        commit(shiftId(ids, id, e.key === 'ArrowUp' ? -1 : 1));
      }
    },
  });

  return { itemProps, handleProps, dragging: drag.id };
}
