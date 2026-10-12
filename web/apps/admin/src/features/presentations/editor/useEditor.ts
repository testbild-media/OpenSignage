import { useSyncExternalStore } from 'react';
import type { EditorSnapshot, EditorStore } from './store';

export function useEditor(store: EditorStore): EditorSnapshot {
  return useSyncExternalStore(store.subscribe, store.getSnapshot);
}
