// Admin live events over WebSocket (/api/v1/ws). Pages subscribe to topics and reload
// their data; the connection state drives the "view may be out of date" banner.
import { useEffect, useRef, useSyncExternalStore } from 'react';

export interface ServerEvent {
  topic: string;
  kind?: string;
  id?: string;
  zoneId?: string;
  data?: unknown;
}

type Listener = (e: ServerEvent) => void;

const listeners = new Set<Listener>();
const stateListeners = new Set<() => void>();
let connected = false;
let socket: WebSocket | null = null;
let retry = 1000;
let started = false;

function setConnected(v: boolean): void {
  if (connected === v) return;
  connected = v;
  for (const l of stateListeners) l();
}

function open(): void {
  if (!started || socket) return;
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/api/v1/ws`);
  socket = ws;
  // Handlers of a replaced or stopped socket must not touch the state (a late close of an
  // old socket would otherwise show "no connection" while the new one is open).
  ws.onopen = () => {
    if (socket !== ws) return;
    retry = 1000;
    setConnected(true);
    // Everything may have changed while we were away.
    for (const l of listeners) l({ topic: '*', kind: 'resync' });
  };
  ws.onmessage = (ev) => {
    if (socket !== ws) return;
    try {
      const e = JSON.parse(String(ev.data)) as ServerEvent;
      for (const l of listeners) l(e);
    } catch (err) {
      console.error('bad event', err);
    }
  };
  ws.onclose = () => {
    if (socket !== ws) return;
    socket = null;
    setConnected(false);
    if (!started) return; // stopped (logout): no reconnect
    window.setTimeout(open, retry);
    retry = Math.min(retry * 2, 30000);
  };
}

export function startEvents(): void {
  if (started) return;
  started = true;
  open();
}

export function stopEvents(): void {
  started = false;
  const ws = socket;
  socket = null;
  setConnected(false);
  ws?.close();
}

/** Current connection state (tests and non-React code). */
export function isConnected(): boolean {
  return connected;
}

/** Whether the live connection is wanted (logged in). */
export function isStarted(): boolean {
  return started;
}

/** Calls fn whenever the connection state changes; returns an unsubscribe function. */
export function onConnectionChange(fn: () => void): () => void {
  stateListeners.add(fn);
  return () => {
    stateListeners.delete(fn);
  };
}

export function useConnected(): boolean {
  return useSyncExternalStore(
    (cb) => {
      stateListeners.add(cb);
      return () => stateListeners.delete(cb);
    },
    () => connected,
  );
}

/** Calls fn for events of the given topics (and for "resync" after reconnects). */
export function useEvents(topics: string[], fn: (e: ServerEvent) => void): void {
  const ref = useRef(fn);
  ref.current = fn;
  const key = topics.join(',');
  useEffect(() => {
    const set = new Set(key.split(','));
    const l: Listener = (e) => {
      if (e.topic === '*' || set.has(e.topic)) ref.current(e);
    };
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  }, [key]);
}
